package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/briqt/kiro-think/internal/cert"
	"github.com/briqt/kiro-think/internal/config"
)

const DefaultAlias = "kt"

// IsInitialized returns true if config and CA cert exist.
func IsInitialized() bool {
	_, err1 := os.Stat(config.Path())
	_, err2 := os.Stat(filepath.Join(config.Dir(), "ca.crt"))
	return err1 == nil && err2 == nil
}

// AutoInit performs fully automatic first-time setup with no user interaction.
// Called implicitly when any command runs and config doesn't exist yet.
func AutoInit() error {
	if IsInitialized() {
		return nil
	}
	fmt.Println("🧠 First run detected, initializing kiro-think...")
	fmt.Println()

	cfg := config.Default()

	// Auto-detect upstream proxy from environment
	cfg.Upstream = detectUpstream()

	// Save config
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Printf("  ✅ Config: %s\n", config.Path())

	// Generate CA cert
	if _, err := cert.NewManager(); err != nil {
		return fmt.Errorf("generate cert: %w", err)
	}
	fmt.Printf("  ✅ CA cert: %s\n", filepath.Join(config.Dir(), "ca.crt"))

	// Write shell alias
	exePath := resolveExePath()
	rcFile := shellRC(detectShell())
	alias := DefaultAlias
	aliasLine := fmt.Sprintf("alias %s='%s run-kiro'", alias, exePath)

	if rcFile != "" && !aliasExists(rcFile) {
		if err := appendAlias(rcFile, aliasLine); err == nil {
			fmt.Printf("  ✅ Alias `%s` added to %s\n", alias, rcFile)
		}
	}

	// Summary
	upstream := cfg.Upstream
	if upstream == "" {
		upstream = "direct"
	}
	fmt.Println()
	fmt.Printf("  Upstream:  %s\n", upstream)
	fmt.Printf("  Level:     %s (%d tokens)\n", cfg.Thinking.Level, cfg.Thinking.Budget)
	fmt.Printf("  Listen:    %s\n", cfg.Listen)
	fmt.Println()
	fmt.Println("  Usage:")
	fmt.Printf("    %s                       launch kiro-cli with thinking injection\n", alias)
	fmt.Println("    kiro-think level <LEVEL>  change thinking level (low/medium/high/xhigh/max)")
	fmt.Println("    kiro-think setup          reconfigure interactively")
	fmt.Println("    kiro-think status         check proxy status")
	fmt.Println()
	fmt.Printf("  Run `source %s` to activate the alias in current shell.\n", rcFile)
	fmt.Println()
	return nil
}

// InteractiveSetup walks the user through full configuration.
// startFn is called to start the daemon if the user agrees.
func InteractiveSetup(startFn func() error) error {
	fmt.Println("🧠 kiro-think setup")
	fmt.Println()

	exePath := resolveExePath()
	cfg := config.Default()

	// Load existing config as defaults if present
	if existing, err := config.Load(); err == nil {
		cfg = existing
	}

	// 1. Listen port
	port := "8960"
	if i := strings.LastIndex(cfg.Listen, ":"); i >= 0 {
		port = cfg.Listen[i+1:]
	}
	port = prompt("Listen port", port)
	cfg.Listen = ":" + port

	// 2. Upstream proxy
	fmt.Println()
	detected := detectUpstream()
	defaultUp := cfg.Upstream
	if defaultUp == "" && detected != "" {
		defaultUp = detected
		fmt.Printf("  Detected upstream proxy: %s\n", detected)
	}
	fmt.Println("  Leave empty for direct connection.")
	cfg.Upstream = prompt("Upstream proxy (host:port)", defaultUp)

	// 3. Thinking level
	fmt.Println()
	fmt.Println("  Thinking levels:")
	for _, l := range []string{"low", "medium", "high", "xhigh", "max"} {
		marker := " "
		if l == cfg.Thinking.Level {
			marker = "→"
		}
		fmt.Printf("    %s %-8s %d tokens\n", marker, l, config.ThinkingLevels[l])
	}
	level := prompt("Thinking level", cfg.Thinking.Level)
	if !cfg.SetLevel(level) {
		fmt.Printf("  Invalid level %q, keeping %s\n", level, cfg.Thinking.Level)
	}

	// 4. Thinking mode
	fmt.Println()
	fmt.Println("  Modes: enabled (fixed budget) | adaptive (effort-based)")
	mode := prompt("Thinking mode", cfg.Thinking.Mode)
	if mode == "enabled" || mode == "adaptive" {
		cfg.Thinking.Mode = mode
	}

	// 5. Save config
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Printf("\n  ✅ Config saved to %s\n", config.Path())

	// 6. Generate CA cert
	fmt.Println("  Generating CA certificate...")
	if _, err := cert.NewManager(); err != nil {
		return fmt.Errorf("generate cert: %w", err)
	}
	fmt.Printf("  ✅ CA cert ready\n")

	// 7. Shell alias
	fmt.Println()
	shell := detectShell()
	rcFile := shellRC(shell)
	alias := DefaultAlias
	if aliasExists(rcFile) {
		fmt.Printf("  Alias already exists in %s\n", rcFile)
		alias = prompt("Alias name (or empty to skip)", "")
		if alias == "" {
			goto startDaemon
		}
		// Remove old alias first
		removeAlias(rcFile)
	} else {
		alias = prompt("Alias name", DefaultAlias)
	}
	{
		aliasLine := fmt.Sprintf("alias %s='%s run-kiro'", alias, exePath)
		if rcFile != "" {
			if err := appendAlias(rcFile, aliasLine); err == nil {
				fmt.Printf("  ✅ Alias `%s` added to %s\n", alias, rcFile)
				fmt.Printf("     Run: source %s\n", rcFile)
			} else {
				fmt.Printf("  ⚠️  Could not write to %s: %v\n", rcFile, err)
				fmt.Printf("     Add manually: %s\n", aliasLine)
			}
		}
	}

startDaemon:
	// 8. Start daemon
	fmt.Println()
	if askYN("Start the proxy now?", true) {
		if startFn != nil {
			if err := startFn(); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠️  start failed: %v\n", err)
				fmt.Println("  Run manually: kiro-think start")
			} else {
				fmt.Println("  ✅ Proxy started")
			}
		}
	}

	fmt.Println()
	fmt.Println("🎉 Setup complete!")
	return nil
}

// --- helpers ---

func detectUpstream() string {
	for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		val := os.Getenv(env)
		if val == "" {
			continue
		}
		// Strip http:// prefix, extract host:port
		val = strings.TrimPrefix(val, "http://")
		val = strings.TrimPrefix(val, "https://")
		val = strings.TrimSuffix(val, "/")
		// Don't use self as upstream
		if strings.Contains(val, "8960") {
			continue
		}
		return val
	}
	return ""
}

func resolveExePath() string {
	// Prefer the PATH-resolved location (e.g. ~/go/bin/kiro-think from go install)
	// over os.Executable (which may point to a temp build directory).
	if p, err := exec.LookPath("kiro-think"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if abs, err := filepath.Abs(exe); err == nil {
		return abs
	}
	return exe
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

func aliasExists(rcFile string) bool {
	if rcFile == "" {
		return false
	}
	data, err := os.ReadFile(rcFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "kiro-think")
}

func appendAlias(rcFile, aliasLine string) error {
	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# kiro-think: launch kiro-cli with thinking injection\n%s\n", aliasLine)
	return err
}

func removeAlias(rcFile string) {
	data, err := os.ReadFile(rcFile)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		if strings.Contains(line, "kiro-think") {
			continue
		}
		out = append(out, line)
	}
	os.WriteFile(rcFile, []byte(strings.Join(out, "\n")), 0644)
}

func prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("  %s: ", label)
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
	fmt.Printf("  %s %s ", question, hint)
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}
