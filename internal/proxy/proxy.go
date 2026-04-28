package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
	"github.com/briqt/kiro-think/internal/inject"
)

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

// dialRemote connects to host:port either directly or via upstream proxy.
func (s *Server) dialRemote(hostport string, cfg *config.Config) (net.Conn, error) {
	if cfg.Upstream == "" {
		return net.Dial("tcp", hostport)
	}
	// Via upstream CONNECT
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

	// MITM: TLS terminate, inspect, forward
	clientConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	tlsConn := tls.Server(clientConn, &tls.Config{
		GetCertificate: s.certMgr.GetCertificate,
	})
	if err := tlsConn.Handshake(); err != nil {
		clientConn.Close()
		return
	}

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
	body, _ := io.ReadAll(req.Body)
	req.Body.Close()

	// Inject thinking tags
	target := req.Header.Get("X-Amz-Target")
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

	// Connect to target
	rawConn, err := s.dialRemote(host+":443", cfg)
	if err != nil {
		log.Printf("dial error: %v", err)
		return
	}

	tlsUp := tls.Client(rawConn, &tls.Config{ServerName: host})
	if err := tlsUp.Handshake(); err != nil {
		rawConn.Close()
		log.Printf("TLS error: %v", err)
		return
	}

	req.Header.Del("Accept-Encoding")
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	req.Host = host
	req.URL.Scheme = "https"
	req.URL.Host = host
	req.RequestURI = ""
	req.Body = io.NopCloser(bytes.NewReader(body))

	if err := req.Write(tlsUp); err != nil {
		tlsUp.Close()
		return
	}

	// For chat requests: raw-pipe entire TLS stream to preserve chunked/event-stream
	// encoding, while teeing to capture token info.
	// For other requests: use http.ReadResponse for clean handling.
	if isChat {
		var captured bytes.Buffer
		buf := make([]byte, 32*1024)
		for {
			tlsUp.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, readErr := tlsUp.Read(buf)
			if n > 0 {
				clientConn.Write(buf[:n])
				captured.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
		if s.cfg.Load().Debug {
			model := extractModelFromEventStream(captured.Bytes())
			if model == "" {
				model = modelID
			}
			log.Printf("  [debug] ← %dB→%dB model=%s", len(body), captured.Len(), model)
		}
	} else {
		upResp, err := http.ReadResponse(bufio.NewReader(tlsUp), req)
		if err == nil {
			upResp.Write(clientConn)
			if s.cfg.Load().Debug {
				log.Printf("  [debug] ← %d %s", upResp.StatusCode, target)
			}
		}
	}
	tlsUp.Close()
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

// extractModelFromEventStream scans AWS Event Stream binary data for modelId.
func extractModelFromEventStream(data []byte) string {
	s := string(data)
	if i := strings.Index(s, `"modelId":"`); i >= 0 {
		sub := s[i+len(`"modelId":"`):]
		if end := strings.Index(sub, `"`); end >= 0 {
			return sub[:end]
		}
	}
	return ""
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
