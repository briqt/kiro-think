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
	case "run-kiro":
		cmdRunKiro()
	case "setup":
		cmdSetup()
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
  run-kiro        Launch kiro-cli through the proxy (auto-sets env vars)
  setup           Interactive first-time setup (config, certs, alias, start)
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

	// Extract port from listen address
	port := "8960"
	if i := strings.LastIndex(cfg.Listen, ":"); i >= 0 {
		port = cfg.Listen[i+1:]
	}

	combinedCA := filepath.Join(config.Dir(), "combined-ca.crt")
	proxyURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Find kiro-cli
	kiroCli, err := exec.LookPath("kiro-cli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiro-cli not found in PATH\n")
		os.Exit(1)
	}

	// Build args: pass through everything after "run-kiro"
	args := []string{kiroCli, "chat"}
	if len(os.Args) > 2 {
		args = append(args, os.Args[2:]...)
	}

	env := os.Environ()
	env = append(env,
		"SSL_CERT_FILE="+combinedCA,
		"HTTPS_PROXY="+proxyURL,
		"HTTP_PROXY="+proxyURL,
	)

	fmt.Printf("launching kiro-cli (level: %s, proxy: %s)\n", cfg.Thinking.Level, proxyURL)
	err = syscall.Exec(kiroCli, args, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec kiro-cli: %v\n", err)
		os.Exit(1)
	}
}

func cmdSetup() {
	fmt.Println("🧠 kiro-think setup")
	fmt.Println()

	// Resolve absolute path of this binary
	exePath, _ := os.Executable()
	exePath, _ = filepath.EvalSymlinks(exePath)
	absExe, err := filepath.Abs(exePath)
	if err == nil {
		exePath = absExe
	}

	cfg := config.Default()

	// If config already exists, ask whether to reconfigure
	if _, err := os.Stat(config.Path()); err == nil {
		existing, _ := config.Load()
		if existing != nil {
			if !askYN("Config already exists at "+config.Path()+". Reconfigure?", false) {
				cfg = existing
				goto skipConfig
			}
			cfg = existing
		}
	}

	// Step 1: Listen port
	{
		port := prompt("Listen port", "8960")
		cfg.Listen = ":" + port
	}

	// Step 2: Upstream proxy
	{
		fmt.Println()
		fmt.Println("Upstream proxy (if you use a proxy to access the internet).")
		fmt.Println("Leave empty for direct connection (most users).")
		cfg.Upstream = prompt("Upstream proxy (host:port)", cfg.Upstream)
	}

	// Step 3: Thinking level
	{
		fmt.Println()
		fmt.Println("Thinking levels:")
		levels := []string{"low", "medium", "high", "xhigh", "max"}
		for _, l := range levels {
			fmt.Printf("  %-8s %d tokens\n", l, config.ThinkingLevels[l])
		}
		level := prompt("Default thinking level", "max")
		if !cfg.SetLevel(level) {
			fmt.Printf("  invalid level %q, using max\n", level)
			cfg.SetLevel("max")
		}
	}

	// Step 4: Thinking mode
	{
		fmt.Println()
		fmt.Println("Thinking mode:")
		fmt.Println("  enabled   - fixed budget tokens (recommended)")
		fmt.Println("  adaptive  - effort-based, model decides depth")
		mode := prompt("Thinking mode", "enabled")
		if mode == "adaptive" || mode == "enabled" {
			cfg.Thinking.Mode = mode
		} else {
			fmt.Printf("  invalid mode %q, using enabled\n", mode)
			cfg.Thinking.Mode = "enabled"
		}
	}

	// Save config
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n✅ Config saved to %s\n", config.Path())

skipConfig:
	// Step 5: Generate CA cert
	fmt.Println()
	fmt.Println("Generating CA certificate...")
	if _, err := cert.NewManager(); err != nil {
		fmt.Fprintf(os.Stderr, "error generating cert: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ CA cert: %s\n", filepath.Join(config.Dir(), "ca.crt"))
	fmt.Printf("✅ Combined CA: %s\n", filepath.Join(config.Dir(), "combined-ca.crt"))

	// Step 6: Shell alias
	fmt.Println()
	shell := detectShell()
	rcFile := shellRC(shell)
	aliasLine := fmt.Sprintf("alias kiro='%s run-kiro'", exePath)

	if rcFile != "" {
		// Check if alias already exists
		if content, err := os.ReadFile(rcFile); err == nil && strings.Contains(string(content), "kiro-think") {
			fmt.Printf("✅ Shell alias already configured in %s\n", rcFile)
		} else if askYN(fmt.Sprintf("Add alias to %s?", rcFile), true) {
			f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			} else {
				fmt.Fprintf(f, "\n# kiro-think: launch kiro-cli with thinking injection\n%s\n", aliasLine)
				f.Close()
				fmt.Printf("✅ Alias added to %s\n", rcFile)
				fmt.Printf("   Run: source %s\n", rcFile)
			}
		}
	} else {
		fmt.Println("Could not detect shell rc file. Add this manually:")
		fmt.Printf("  %s\n", aliasLine)
	}

	// Step 7: Start daemon
	fmt.Println()
	if askYN("Start the proxy now?", true) {
		if pid, running := daemon.IsRunning(); running {
			fmt.Printf("✅ Already running (pid %d)\n", pid)
		} else if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		} else {
			fmt.Println("✅ Proxy started")
		}
	}

	// Done
	fmt.Println()
	fmt.Println("🎉 Setup complete! Usage:")
	fmt.Println()
	fmt.Println("  kiro                    # launch kiro-cli with thinking injection")
	fmt.Println("  kiro-think level max    # change thinking level")
	fmt.Println("  kiro-think status       # check proxy status")
}

func prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal
	}
	return input
}

func askYN(question string, defaultYes bool) bool {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s ", question, hint)
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}

func detectShell() string {
	shell := os.Getenv("SHELL")
	if strings.Contains(shell, "zsh") {
		return "zsh"
	}
	if strings.Contains(shell, "fish") {
		return "fish"
	}
	return "bash"
}

func shellRC(shell string) string {
	home, _ := os.UserHomeDir()
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		return filepath.Join(home, ".bashrc")
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
