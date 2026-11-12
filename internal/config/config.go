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

// ExtendPath adds the usual places of ffmpeg and tesseract to this process's PATH when they
// exist and are not on it: an agent or app started from the Dock or before the tools were
// installed has a short or stale PATH, and "ffmpeg not found" would be the first thing a new
// user sees.
func ExtendPath() {
	var dirs []string
	switch runtime.GOOS {
	case "darwin":
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	case "windows":
		local, pf := os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles")
		if local != "" {
			dirs = append(dirs, filepath.Join(local, "Microsoft", "WinGet", "Links"))
			// winget's ffmpeg (Gyan.FFmpeg) keeps its programs in a versioned folder.
			if m, _ := filepath.Glob(filepath.Join(local, "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "ffmpeg-*", "bin")); len(m) > 0 {
				dirs = append(dirs, m[len(m)-1])
			}
		}
		if pf != "" {
			dirs = append(dirs, filepath.Join(pf, "Tesseract-OCR"), filepath.Join(pf, "ffmpeg", "bin"))
		}
	default:
		dirs = []string{"/usr/local/bin"}
	}
	path := os.Getenv("PATH")
	have := map[string]bool{}
	for _, d := range filepath.SplitList(path) {
		have[strings.ToLower(filepath.Clean(d))] = true
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() && !have[strings.ToLower(filepath.Clean(d))] {
			path += string(os.PathListSeparator) + d
		}
	}
	os.Setenv("PATH", path)
}
