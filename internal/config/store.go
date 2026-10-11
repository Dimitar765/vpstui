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
		return nil, fmt.Errorf("corrupt hosts file %s: %w", p, err)
	}
	return hosts, nil
}

func Save(hosts []models.Host) error {
	p, err := hostsPath()
	if err != nil {
		return err
	}
	return writeJSON(p, hosts)
}

// writeJSON replaces a file atomically at 0600. Every vpstui config document
// goes through here: a crash mid-write must never leave a half-written hosts or
// tags file that the next start would refuse to load.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
