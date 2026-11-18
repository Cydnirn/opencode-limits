// Package config resolves per-operating-system paths for opencode-limits.
//
// The credential file this tool reads and writes is owned by the
// opencode-console CLI. Its location is resolved here to match that CLI's own
// resolution exactly, so both tools find the same file:
//
//	Linux   $XDG_DATA_HOME/opencode-console else ~/.local/share/opencode-console
//	macOS   ~/Library/Application Support/opencode-console
//	Windows %LOCALAPPDATA%\opencode-console
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AppName is the directory name used by the opencode-console CLI.
const AppName = "opencode-console"

// ToolName is this tool's own directory name, used for config and cache.
const ToolName = "opencode-limits"

// DefaultServer is the OpenCode Console API origin.
const DefaultServer = "https://opencode.ai/console"

// Paths holds every filesystem location the tool needs.
type Paths struct {
	// CredentialFile is the opencode-console credential written by the CLI.
	CredentialFile string
	// ConfigFile is this tool's own configuration (allocations).
	ConfigFile string
	// CacheDir is where response snapshots may be cached.
	CacheDir string
	// Server is the Console API origin, overridable by env.
	Server string
}

// OS is indirected so tests can assert every platform's layout from any host.
type OS = string

const (
	Linux   OS = "linux"
	Darwin  OS = "darwin"
	Windows OS = "windows"
)

// Env is the slice of the environment that affects resolution.
type Env struct {
	GOOS          OS
	Home          string
	XDGDataHome   string
	XDGConfigHome string
	XDGCacheHome  string
	LocalAppData  string
	Server        string
}

// FromOSEnv reads the real process environment.
func FromOSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		GOOS:          runtime.GOOS,
		Home:          home,
		XDGDataHome:   os.Getenv("XDG_DATA_HOME"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		XDGCacheHome:  os.Getenv("XDG_CACHE_HOME"),
		LocalAppData:  os.Getenv("LOCALAPPDATA"),
		Server:        os.Getenv("OPENCODE_CONSOLE_SERVER"),
	}
}

// DataDir returns the per-OS data directory for app.
func (e Env) DataDir(app string) (string, error) {
	if e.Home == "" {
		return "", fmt.Errorf("cannot resolve home directory")
	}
	switch e.GOOS {
	case Windows:
		base := e.LocalAppData
		if base == "" {
			base = filepath.Join(e.Home, "AppData", "Local")
		}
		return filepath.Join(base, app), nil
	case Darwin:
		return filepath.Join(e.Home, "Library", "Application Support", app), nil
	default: // linux and every other unix
		base := e.XDGDataHome
		if base == "" {
			base = filepath.Join(e.Home, ".local", "share")
		}
		return filepath.Join(base, app), nil
	}
}

// ConfigDir returns the per-OS configuration directory for app.
func (e Env) ConfigDir(app string) (string, error) {
	if e.Home == "" {
		return "", fmt.Errorf("cannot resolve home directory")
	}
	switch e.GOOS {
	case Windows:
		base := e.LocalAppData
		if base == "" {
			base = filepath.Join(e.Home, "AppData", "Local")
		}
		return filepath.Join(base, app), nil
	case Darwin:
		return filepath.Join(e.Home, "Library", "Application Support", app), nil
	default:
		base := e.XDGConfigHome
		if base == "" {
			base = filepath.Join(e.Home, ".config")
		}
		return filepath.Join(base, app), nil
	}
}

// CacheDirFor returns the per-OS cache directory for app.
func (e Env) CacheDirFor(app string) (string, error) {
	if e.Home == "" {
		return "", fmt.Errorf("cannot resolve home directory")
	}
	switch e.GOOS {
	case Windows:
		base := e.LocalAppData
		if base == "" {
			base = filepath.Join(e.Home, "AppData", "Local")
		}
		return filepath.Join(base, app, "Cache"), nil
	case Darwin:
		return filepath.Join(e.Home, "Library", "Caches", app), nil
	default:
		base := e.XDGCacheHome
		if base == "" {
			base = filepath.Join(e.Home, ".cache")
		}
		return filepath.Join(base, app), nil
	}
}

// Resolve computes all paths, honouring OPENCODE_CONSOLE_SERVER.
func (e Env) Resolve() (Paths, error) {
	dataDir, err := e.DataDir(AppName)
	if err != nil {
		return Paths{}, err
	}
	cfgDir, err := e.ConfigDir(ToolName)
	if err != nil {
		return Paths{}, err
	}
	cacheDir, err := e.CacheDirFor(ToolName)
	if err != nil {
		return Paths{}, err
	}
	server := e.Server
	if server == "" {
		server = DefaultServer
	}
	return Paths{
		CredentialFile: filepath.Join(dataDir, "credential.json"),
		ConfigFile:     filepath.Join(cfgDir, "config.toml"),
		CacheDir:       cacheDir,
		Server:         server,
	}, nil
}
