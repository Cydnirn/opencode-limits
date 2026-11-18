package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is this tool's own configuration file.
type Config struct {
	// Allocations maps a model ID to its share of the plan budget in USD per
	// monthly period. Values are scaled proportionally into the 5-hour and
	// weekly windows at read time.
	Allocations map[string]float64 `toml:"allocations"`
	// RefreshSeconds is the auto-refresh cadence for the TUI.
	RefreshSeconds int `toml:"refresh_seconds"`
	// WarningPercent and DangerPercent drive meter tone thresholds.
	WarningPercent int `toml:"warning_percent"`
	DangerPercent  int `toml:"danger_percent"`
}

// Defaults returns the configuration used when no file exists.
func Defaults() Config {
	return Config{
		Allocations:    map[string]float64{},
		RefreshSeconds: 60,
		WarningPercent: 70,
		DangerPercent:  95,
	}
}

// ErrNoConfig reports that no configuration file exists yet.
var ErrNoConfig = errors.New("no config file")

// Load reads the configuration at path, applying defaults for absent fields.
func Load(path string) (Config, error) {
	cfg := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, ErrNoConfig
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	md, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	// Report unknown keys: a typo in a model ID silently loses an allocation,
	// which is exactly the failure this tool exists to prevent.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return cfg, fmt.Errorf("config %s has unknown keys: %v", path, keys)
	}
	if cfg.Allocations == nil {
		cfg.Allocations = map[string]float64{}
	}
	if cfg.RefreshSeconds <= 0 {
		cfg.RefreshSeconds = 60
	}
	if cfg.WarningPercent <= 0 || cfg.WarningPercent >= 100 {
		cfg.WarningPercent = 70
	}
	if cfg.DangerPercent <= 0 || cfg.DangerPercent >= 100 {
		cfg.DangerPercent = 95
	}
	if cfg.WarningPercent > cfg.DangerPercent {
		cfg.WarningPercent, cfg.DangerPercent = cfg.DangerPercent, cfg.WarningPercent
	}
	return cfg, nil
}

// LoadOrDefault returns the config at path, or defaults when absent or invalid.
func LoadOrDefault(path string) Config {
	cfg, err := Load(path)
	if err != nil {
		return Defaults()
	}
	return cfg
}

// Write renders the config as TOML and writes it atomically.
func Write(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("# opencode-limits configuration\n#\n")
	sb.WriteString("# allocations: model ID -> USD per monthly period.\n")
	sb.WriteString("# Each value is scaled proportionally into the 5-hour and weekly\n")
	sb.WriteString("# windows using the live plan limits, so one number per model is enough.\n#\n")
	sb.WriteString("# Allocations are a planning overlay only. They do not change anything in\n")
	sb.WriteString("# OpenCode; they tell this tool how you want to divide the plan budget.\n\n")

	fmt.Fprintf(&sb, "refresh_seconds = %d\n", cfg.RefreshSeconds)
	fmt.Fprintf(&sb, "warning_percent = %d\n", cfg.WarningPercent)
	fmt.Fprintf(&sb, "danger_percent  = %d\n\n", cfg.DangerPercent)

	sb.WriteString("[allocations]\n")
	models := make([]string, 0, len(cfg.Allocations))
	for m := range cfg.Allocations {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		fmt.Fprintf(&sb, "%q = %g\n", m, cfg.Allocations[m])
	}
	return WriteFileAtomic(path, []byte(sb.String()))
}

// WriteFileAtomic writes b to path via a temp file in the same directory and a
// rename, so a crash mid-write cannot truncate an existing file. On Windows a
// rename over an existing file fails, so the target is removed first.
func WriteFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort: if the rename succeeded this is already gone.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Windows cannot rename onto an existing file.
		if removeErr := os.Remove(path); removeErr == nil {
			if err2 := os.Rename(tmpName, path); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	return nil
}
