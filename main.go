package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
	"github.com/briqt/kiro-think/internal/daemon"
	"github.com/briqt/kiro-think/internal/proxy"
	"github.com/briqt/kiro-think/internal/setup"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	// Commands that don't need init
	switch cmd {
	case "version":
		fmt.Printf("kiro-think %s (commit: %s, built: %s)\n", version, commit, date)
		return
	case "help", "-h", "--help":
		usage()
		return
	case "init":
		if err := setup.AutoInit(); err != nil {
			fmt.Fprintf(os.Stderr, "init error: %v\n", err)
			os.Exit(1)
		}
		return
	case "setup":
		if err := setup.InteractiveSetup(func() error {
			daemon.Stop() // stop if running
			return daemon.Start()
		}); err != nil {
			fmt.Fprintf(os.Stderr, "setup error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// All other commands: auto-init on first run
	if !setup.IsInitialized() {
		if err := setup.AutoInit(); err != nil {
			fmt.Fprintf(os.Stderr, "init error: %v\n", err)
			os.Exit(1)
		}
	}

	switch cmd {
	case "start":
		if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "stop":
		if err := daemon.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "restart":
		daemon.Stop()
		if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "status":
		cmdStatus()
	case "level":
		cmdLevel()
	case "run-kiro":
		cmdRunKiro()
	case "run":
		cmdRun()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`kiro-think - MITM proxy for Kiro CLI thinking budget injection

Usage:
  kiro-think <command>

Commands:
  start           Start the proxy daemon
  stop            Stop the proxy daemon
  restart         Restart the proxy daemon
  status          Show proxy status
  level [LEVEL]   Get or set thinking level (low/medium/high/xhigh/max)
  run-kiro        Launch kiro-cli through the proxy (auto-starts daemon)
  init            Auto-initialize (config, certs, alias) with defaults
  setup           Interactive reconfiguration
  run             Run proxy in foreground (for debugging)
  version         Show version info
  help            Show this help
`)
}

func cmdStatus() {
	cfg, _ := config.Load()
	pid, running := daemon.IsRunning()
	if running {
		fmt.Printf("status:   running (pid %d)\n", pid)
	} else {
		fmt.Println("status:   stopped")
	}
	fmt.Printf("listen:   %s\n", cfg.Listen)
	upstream := cfg.Upstream
	if upstream == "" {
		upstream = "(direct)"
	}
	fmt.Printf("upstream: %s\n", upstream)
	fmt.Printf("mode:     %s\n", cfg.Thinking.Mode)
	fmt.Printf("level:    %s\n", cfg.Thinking.Level)
	fmt.Printf("budget:   %d\n", cfg.Thinking.Budget)
}

func cmdLevel() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 3 {
		fmt.Printf("level:  %s\n", cfg.Thinking.Level)
		fmt.Printf("budget: %d\n", cfg.Thinking.Budget)
		fmt.Printf("mode:   %s\n", cfg.Thinking.Mode)
		fmt.Println("\navailable levels:")
		levels := make([]string, 0, len(config.ThinkingLevels))
		for k := range config.ThinkingLevels {
			levels = append(levels, k)
		}
		sort.Slice(levels, func(i, j int) bool {
			return config.ThinkingLevels[levels[i]] < config.ThinkingLevels[levels[j]]
		})
		for _, l := range levels {
			marker := " "
			if l == cfg.Thinking.Level {
				marker = "→"
			}
			fmt.Printf("  %s %-8s %d tokens\n", marker, l, config.ThinkingLevels[l])
		}
		return
	}

	level := strings.ToLower(os.Args[2])
	if !cfg.SetLevel(level) {
		fmt.Fprintf(os.Stderr, "invalid level: %s (use low/medium/high/xhigh/max)\n", level)
		os.Exit(1)
	}
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "save config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("level set to %s (budget: %d)\n", level, cfg.Thinking.Budget)

	if err := daemon.SendHUP(); err == nil {
		fmt.Println("daemon reloaded")
	}
}

func cmdRunKiro() {
	cfg, _ := config.Load()

	// Auto-start daemon if not running
	if _, running := daemon.IsRunning(); !running {
		fmt.Println("starting proxy...")
		if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "error starting proxy: %v\n", err)
			os.Exit(1)
		}
	}

	port := "8960"
	if i := strings.LastIndex(cfg.Listen, ":"); i >= 0 {
		port = cfg.Listen[i+1:]
	}

	combinedCA := filepath.Join(config.Dir(), "combined-ca.crt")
	proxyURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	kiroCli, err := exec.LookPath("kiro-cli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-cli not found in PATH\n")
		os.Exit(1)
	}

	args := []string{kiroCli, "chat"}
	if len(os.Args) > 2 {
		args = append(args, os.Args[2:]...)
	}

	env := os.Environ()
	// Remove existing proxy/cert vars to ensure ours take effect
	filtered := env[:0]
	for _, e := range env {
		upper := strings.ToUpper(e)
		if strings.HasPrefix(upper, "HTTPS_PROXY=") ||
			strings.HasPrefix(upper, "HTTP_PROXY=") ||
			strings.HasPrefix(upper, "SSL_CERT_FILE=") {
			continue
		}
		filtered = append(filtered, e)
	}
	filtered = append(filtered,
		"SSL_CERT_FILE="+combinedCA,
		"HTTPS_PROXY="+proxyURL,
		"HTTP_PROXY="+proxyURL,
	)

	fmt.Printf("launching kiro-cli (level: %s, proxy: %s)\n", cfg.Thinking.Level, proxyURL)
	if err := syscall.Exec(kiroCli, args, filtered); err != nil {
		fmt.Fprintf(os.Stderr, "exec kiro-cli: %v\n", err)
		os.Exit(1)
	}
}

func cmdRun() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	certMgr, err := cert.NewManager()
	if err != nil {
		log.Fatalf("cert: %v", err)
	}

	srv := proxy.New(cfg, certMgr)
	daemon.WritePidSelf()
	defer daemon.RemovePid()

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			newCfg, err := config.Load()
			if err != nil {
				log.Printf("reload config error: %v", err)
				continue
			}
			srv.Reload(newCfg)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-quit
		log.Println("shutting down...")
		srv.Close()
	}()

	log.Printf("kiro-think %s starting: level=%s budget=%d mode=%s",
		version, cfg.Thinking.Level, cfg.Thinking.Budget, cfg.Thinking.Mode)
	if err := srv.ListenAndServe(cfg.Listen); err != nil {
		log.Printf("server: %v", err)
	}
}
