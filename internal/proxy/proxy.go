package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http2"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
	"github.com/briqt/kiro-think/internal/inject"
)

type Server struct {
	cfg      atomic.Pointer[config.Config]
	certMgr  *cert.Manager
	listener net.Listener

	// Per-host upstream transport with h2 support and connection pooling.
	transports sync.Map // host -> *http.Transport
}

func New(cfg *config.Config, certMgr *cert.Manager) *Server {
	s := &Server{certMgr: certMgr}
	s.cfg.Store(cfg)
	return s
}

func (s *Server) Reload(cfg *config.Config) {
	s.cfg.Store(cfg)
	s.transports.Range(func(key, val any) bool {
		val.(*http.Transport).CloseIdleConnections()
		s.transports.Delete(key)
		return true
	})
	log.Printf("config reloaded: level=%s budget=%d mode=%s",
		cfg.Thinking.Level, cfg.Thinking.Budget, cfg.Thinking.Mode)
}

func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	cfg := s.cfg.Load()
	if cfg.Upstream == "" {
		log.Printf("listening on %s (direct mode)", addr)
	} else {
		log.Printf("listening on %s (upstream: %s)", addr, cfg.Upstream)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.handleHTTP)}
	return srv.Serve(ln)
}

func (s *Server) Close() {
	if s.listener != nil {
		s.listener.Close()
	}
}

// getTransport returns a shared, h2-capable http.Transport for the given host.
func (s *Server) getTransport(host string) *http.Transport {
	if v, ok := s.transports.Load(host); ok {
		return v.(*http.Transport)
	}
	cfg := s.cfg.Load()
	t := &http.Transport{
		TLSClientConfig:   &tls.Config{ServerName: host},
		ForceAttemptHTTP2:  true,
		DisableCompression: true,
	}
	if cfg.Upstream != "" {
		t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return s.dialUpstreamTLS(ctx, addr, host)
		}
	}
	http2.ConfigureTransport(t)
	actual, _ := s.transports.LoadOrStore(host, t)
	return actual.(*http.Transport)
}

// dialUpstreamTLS dials through the upstream CONNECT proxy and completes TLS with h2 ALPN.
func (s *Server) dialUpstreamTLS(ctx context.Context, addr, host string) (net.Conn, error) {
	cfg := s.cfg.Load()
	var d net.Dialer
	upConn, err := d.DialContext(ctx, "tcp", cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream dial: %w", err)
	}
	fmt.Fprintf(upConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr)
	resp, err := http.ReadResponse(bufio.NewReader(upConn), nil)
	if err != nil {
		upConn.Close()
		return nil, fmt.Errorf("upstream CONNECT: %w", err)
	}
	if resp.StatusCode != 200 {
		upConn.Close()
		return nil, fmt.Errorf("upstream CONNECT: %s", resp.Status)
	}
	tlsConn := tls.Client(upConn, &tls.Config{
		ServerName: host,
		NextProtos: []string{"h2", "http/1.1"},
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		upConn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
	} else {
		s.handlePlain(w, r)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port, _ := net.SplitHostPort(r.Host)
	if port == "" {
		port = "443"
	}

	cfg := s.cfg.Load()
	isTarget := port == "443" && s.isTarget(host, cfg)

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		return
	}

	if !isTarget {
		s.tunnel(clientConn, r.Host, cfg)
		return
	}

	// MITM: TLS terminate with h2 ALPN, inspect, forward.
	clientConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	tlsConn := tls.Server(clientConn, &tls.Config{
		GetCertificate: s.certMgr.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		clientConn.Close()
		return
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.proxyRequest(w, r, host)
	})

	proto := tlsConn.ConnectionState().NegotiatedProtocol
	if proto == "h2" {
		h2srv := &http2.Server{}
		h2srv.ServeConn(tlsConn, &http2.ServeConnOpts{Handler: handler})
	} else {
		srv := &http.Server{Handler: handler}
		srv.Serve(&singleConnListener{conn: tlsConn})
	}
}

// proxyRequest handles both h1 and h2 requests: inject thinking, forward upstream, stream back.
func (s *Server) proxyRequest(w http.ResponseWriter, r *http.Request, host string) {
	cfg := s.cfg.Load()

	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	target := r.Header.Get("X-Amz-Target")
	isChat := strings.Contains(target, "GenerateAssistantResponse")
	var modelID string
	if isChat {
		var injected bool
		body, injected, modelID = inject.InjectThinking(body, cfg.Thinking)
		if injected {
			log.Printf("💉 [%s] %s", modelID, inject.GeneratePrefix(cfg.Thinking))
		} else if modelID != "" {
			log.Printf("⏭️  [%s] skipped (not in whitelist)", modelID)
		}
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, "https://"+host+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			upReq.Header.Add(k, v)
		}
	}
	upReq.Header.Del("Accept-Encoding")
	upReq.ContentLength = int64(len(body))

	upResp, err := s.getTransport(host).RoundTrip(upReq)
	if err != nil {
		log.Printf("upstream error: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	for k, vv := range upResp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	// Stream with flush for event-stream responses (chat).
	if f, ok := w.(http.Flusher); ok && isChat {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := upResp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				f.Flush()
			}
			if readErr != nil {
				break
			}
		}
	} else {
		io.Copy(w, upResp.Body)
	}

	if cfg.Debug {
		log.Printf("  [debug] ← %d %s (%dB)", upResp.StatusCode, target, len(body))
	}
}

func isHopByHop(k string) bool {
	switch strings.ToLower(k) {
	case "connection", "keep-alive", "proxy-connection",
		"transfer-encoding", "upgrade", "te":
		return true
	}
	return false
}

func (s *Server) isTarget(host string, cfg *config.Config) bool {
	for _, t := range cfg.Targets {
		if strings.EqualFold(host, t) {
			return true
		}
	}
	return false
}

func (s *Server) tunnel(clientConn net.Conn, target string, cfg *config.Config) {
	remoteConn, err := s.dialRemote(target, cfg)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		clientConn.Close()
		return
	}
	clientConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	go io.Copy(remoteConn, clientConn)
	io.Copy(clientConn, remoteConn)
	clientConn.Close()
	remoteConn.Close()
}

func (s *Server) dialRemote(hostport string, cfg *config.Config) (net.Conn, error) {
	if cfg.Upstream == "" {
		return net.Dial("tcp", hostport)
	}
	upConn, err := net.Dial("tcp", cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream dial: %w", err)
	}
	fmt.Fprintf(upConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", hostport, hostport)
	resp, err := http.ReadResponse(bufio.NewReader(upConn), nil)
	if err != nil {
		upConn.Close()
		return nil, fmt.Errorf("upstream CONNECT: %w", err)
	}
	if resp.StatusCode != 200 {
		upConn.Close()
		return nil, fmt.Errorf("upstream CONNECT: %s", resp.Status)
	}
	return upConn, nil
}

func (s *Server) handlePlain(w http.ResponseWriter, r *http.Request) {
	resp, err := http.DefaultTransport.(*http.Transport).RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// singleConnListener wraps a net.Conn as a one-shot net.Listener for http.Server.Serve.
type singleConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var accepted bool
	l.once.Do(func() {
		l.done = make(chan struct{})
		accepted = true
	})
	if accepted {
		return l.conn, nil
	}
	<-l.done
	return nil, io.EOF
}

func (l *singleConnListener) Close() error {
	if l.done != nil {
		select {
		case <-l.done:
		default:
			close(l.done)
		}
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
