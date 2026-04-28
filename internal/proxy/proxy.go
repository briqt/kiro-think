package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync/atomic"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
	"github.com/briqt/kiro-think/internal/inject"
)

// Server is the MITM proxy server.
type Server struct {
	cfg      atomic.Pointer[config.Config]
	certMgr  *cert.Manager
	listener net.Listener
}

func New(cfg *config.Config, certMgr *cert.Manager) *Server {
	s := &Server{certMgr: certMgr}
	s.cfg.Store(cfg)
	return s
}

// Reload updates the running config (called on SIGHUP).
func (s *Server) Reload(cfg *config.Config) {
	s.cfg.Store(cfg)
	log.Printf("config reloaded: level=%s budget=%d mode=%s",
		cfg.Thinking.Level, cfg.Thinking.Budget, cfg.Thinking.Mode)
}

func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	log.Printf("listening on %s", addr)
	srv := &http.Server{Handler: http.HandlerFunc(s.handleHTTP)}
	return srv.Serve(ln)
}

func (s *Server) Close() {
	if s.listener != nil {
		s.listener.Close()
	}
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
		// Plain tunnel through upstream
		s.tunnel(clientConn, r.Host, cfg)
		return
	}

	// MITM: TLS terminate, inspect, forward
	clientConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))

	tlsConn := tls.Server(clientConn, &tls.Config{
		GetCertificate: s.certMgr.GetCertificate,
	})
	if err := tlsConn.Handshake(); err != nil {
		clientConn.Close()
		return
	}

	// Read HTTP requests from decrypted connection
	br := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			break
		}
		s.forwardRequest(tlsConn, req, host, cfg)
	}
	tlsConn.Close()
}

func (s *Server) isTarget(host string, cfg *config.Config) bool {
	for _, t := range cfg.Targets {
		if strings.EqualFold(host, t) {
			return true
		}
	}
	return false
}

func (s *Server) forwardRequest(clientConn net.Conn, req *http.Request, host string, cfg *config.Config) {
	// Read request body
	body, _ := io.ReadAll(req.Body)
	req.Body.Close()

	// Inject thinking tags if this is a GenerateAssistantResponse request
	target := req.Header.Get("X-Amz-Target")
	injected := false
	if strings.Contains(target, "GenerateAssistantResponse") {
		body, injected = inject.InjectThinking(body, cfg.Thinking)
		if injected {
			log.Printf("💉 injected: %s", inject.GeneratePrefix(cfg.Thinking))
		}
	}

	// Connect to upstream via CONNECT tunnel
	upConn, err := net.Dial("tcp", cfg.Upstream)
	if err != nil {
		log.Printf("upstream dial error: %v", err)
		return
	}
	fmt.Fprintf(upConn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", host, host)
	resp, err := http.ReadResponse(bufio.NewReader(upConn), nil)
	if err != nil || resp.StatusCode != 200 {
		upConn.Close()
		log.Printf("upstream CONNECT failed")
		return
	}

	// TLS to upstream
	tlsUp := tls.Client(upConn, &tls.Config{ServerName: host})
	if err := tlsUp.Handshake(); err != nil {
		upConn.Close()
		log.Printf("upstream TLS error: %v", err)
		return
	}

	// Remove accept-encoding to get uncompressed response
	req.Header.Del("Accept-Encoding")
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	req.Host = host
	req.URL.Scheme = "https"
	req.URL.Host = host
	req.RequestURI = ""

	// Send request
	if err := req.Write(tlsUp); err != nil {
		tlsUp.Close()
		return
	}
	tlsUp.Write(body)

	// Read response and forward to client
	upResp, err := http.ReadResponse(bufio.NewReader(tlsUp), req)
	if err != nil {
		tlsUp.Close()
		return
	}
	defer upResp.Body.Close()

	respBody, _ := io.ReadAll(upResp.Body)
	log.Printf("← %d (%dB) %s", upResp.StatusCode, len(respBody), target)

	// Write response back to client
	respBytes, _ := httputil.DumpResponse(upResp, false)
	clientConn.Write(respBytes)
	clientConn.Write(respBody)
	tlsUp.Close()
}

func (s *Server) tunnel(clientConn net.Conn, target string, cfg *config.Config) {
	upConn, err := net.Dial("tcp", cfg.Upstream)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		clientConn.Close()
		return
	}
	fmt.Fprintf(upConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(upConn), nil)
	if err != nil || resp.StatusCode != 200 {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		clientConn.Close()
		upConn.Close()
		return
	}
	clientConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	go io.Copy(upConn, clientConn)
	io.Copy(clientConn, upConn)
	clientConn.Close()
	upConn.Close()
}

func (s *Server) handlePlain(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	resp, err := http.DefaultTransport.(*http.Transport).RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	_ = cfg // plain HTTP just forwards
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
