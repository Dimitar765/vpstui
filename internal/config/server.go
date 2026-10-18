package config

import (
	"os"
	"path/filepath"
)

// ServerDataDir returns (and creates, 0700) the vpstui-server XDG data
// directory — the daemon-side analogue of DataDir(), kept in a separate file
// so the TUI's config store never has to know the daemon exists.
func ServerDataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "vpstui-server")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
