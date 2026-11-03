// Package config: where Agent Mask keeps its files (the API token), per OS.
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// App is the folder name; its upper-case form names the override variable (AGENT_MASK_CONFIG_DIR).
var App = "agent-mask"

// Dir is %APPDATA%\<app>, ~/Library/Application Support/<app> or $XDG_CONFIG_HOME/<app>;
// <APP>_CONFIG_DIR overrides it.
func Dir() string {
	env := strings.ToUpper(strings.ReplaceAll(App, "-", "_")) + "_CONFIG_DIR"
	if d := os.Getenv(env); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, App)
		}
		return filepath.Join(home, "AppData", "Roaming", App)
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", App)
	default:
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			return filepath.Join(x, App)
		}
		return filepath.Join(home, ".config", App)
	}
}
