package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"vpstui/internal/models"
)

func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "vpstui")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// DataDir returns (and creates, 0700) the vpstui XDG data directory plus its
// logs subdirectory — the analogue of Dir() for state that is not config.
func DataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	if err := os.MkdirAll(filepath.Join(base, "vpstui", "logs"), 0700); err != nil {
		return "", err
	}
	return filepath.Join(base, "vpstui"), nil
}

func hostsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hosts.json"), nil
}

func Load() ([]models.Host, error) {
	p, err := hostsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hosts []models.Host
	if err := json.Unmarshal(data, &hosts); err != nil {
		renameAside(p)
		return nil, fmt.Errorf("corrupt hosts file %s — renamed to %s.bad: %w", p, p, err)
	}
	return hosts, nil
}

// renameAside moves a file the loader could not parse out of the way, so the
// original bytes survive as <path>.bad when a later Save writes a fresh file.
// os.Rename replaces any prior .bad.
func renameAside(path string) {
	_ = os.Rename(path, path+".bad")
}

func Save(hosts []models.Host) error {
	p, err := hostsPath()
	if err != nil {
		return err
	}
	return writeJSON(p, hosts)
}

// WriteFileAtomic replaces path with data atomically and durably at perm:
// the temp file is fsynced before the rename and the parent directory after
// it, so a crash can never leave the rename applied with empty or partial
// content. Every vpstui config document goes through here.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

// writeJSON marshals v and stores it atomically at 0600.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0600)
}
