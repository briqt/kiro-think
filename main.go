package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
	"github.com/briqt/kiro-think/internal/daemon"
	"github.com/briqt/kiro-think/internal/proxy"
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

	switch os.Args[1] {
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
	case "run":
		cmdRun()
	case "version":
		fmt.Printf("kiro-think %s (commit: %s, built: %s)\n", version, commit, date)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
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
  run             Run in foreground (for debugging)
  version         Show version info
  help            Show this help

Environment:
  SSL_CERT_FILE=~/.kiro-think/combined-ca.crt
  HTTPS_PROXY=http://127.0.0.1:8960
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
	fmt.Printf("upstream: %s\n", cfg.Upstream)
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
		// Show current level
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

	// Hot-reload running daemon
	if err := daemon.SendHUP(); err == nil {
		fmt.Println("daemon reloaded")
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

	// Handle SIGHUP for config reload
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

	// Handle SIGTERM/SIGINT for graceful shutdown
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
