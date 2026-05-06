package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ThinkingLevel maps effort names to budget tokens.
var ThinkingLevels = map[string]int{
	"low":    4096,
	"medium": 10000,
	"high":   20000,
	"xhigh":  22000,
	"max":    24576,
}

type ThinkingConfig struct {
	Mode   string `json:"mode"`   // "enabled" or "adaptive"
	Level  string `json:"level"`  // low/medium/high/xhigh/max
	Budget int    `json:"budget"` // budget tokens (auto-set from level when mode=enabled)
}

type Config struct {
	Listen   string         `json:"listen"`
	Upstream string         `json:"upstream"`
	Thinking ThinkingConfig `json:"thinking"`
	LogFile  string         `json:"log_file"`
	Targets  []string       `json:"targets"`
	Models   []string       `json:"models"`
	Debug    bool           `json:"debug"`
}

func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kiro-think")
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

func PidPath() string {
	return filepath.Join(Dir(), "kiro-think.pid")
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func Default() *Config {
	return &Config{
		Listen:   ":8960",
		Upstream: "",
		Thinking: ThinkingConfig{
			Mode:   "enabled",
			Level:  "max",
			Budget: 24576,
		},
		LogFile: "~/.kiro-think/kiro-think.log",
		Targets: []string{"q.*.amazonaws.com"},
		Models:  []string{"claude-sonnet-4.5", "claude-sonnet-4.6", "claude-opus-4.5", "claude-opus-4.6", "claude-opus-4.7"},
	}
}

func Load() (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			os.MkdirAll(Dir(), 0755)
			return cfg, Save(cfg)
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.LogFile = expandPath(cfg.LogFile)
	return cfg, nil
}

func Save(cfg *Config) error {
	os.MkdirAll(Dir(), 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(data, '\n'), 0644)
}

// SetLevel updates the thinking level and corresponding budget.
func (c *Config) SetLevel(level string) bool {
	budget, ok := ThinkingLevels[level]
	if !ok {
		return false
	}
	c.Thinking.Level = level
	c.Thinking.Budget = budget
	return true
}
