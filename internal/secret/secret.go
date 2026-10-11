package secret

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
	"vpstui/internal/config"
)

const (
	service      = "vpstui"
	probeService = "vpstui-probe" // probe key only; can never collide with user secrets
)

var (
	mu        sync.Mutex
	keyringOK *bool // lazily probed; nil = unknown
)

func useKeyring() bool {
	mu.Lock()
	defer mu.Unlock()
	if keyringOK == nil {
		ok := keyring.Set(probeService, "__probe__", "1") == nil
		if ok {
			_ = keyring.Delete(probeService, "__probe__")
		}
		keyringOK = &ok
	}
	return *keyringOK
}

func filePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets.json"), nil
}

func readFile() (map[string]string, error) {
	p, err := filePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeFile(m map[string]string) error {
	p, err := filePath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(p, data, 0600)
}

func Set(name, value string) error {
	if useKeyring() {
		if err := keyring.Set(service, name, value); err == nil {
			return nil
		}
	}
	m, err := readFile()
	if err != nil {
		return err
	}
	m[name] = value
	return writeFile(m)
}

func Get(name string) (string, error) {
	if useKeyring() {
		v, err := keyring.Get(service, name)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			// A real keyring failure must not silently read a possibly stale
			// file copy; callers treat any error as "prompt again".
			return "", err
		}
		// Not in the keyring: fall through — the secret may live in the file
		// store because its Set fell back after a keyring write failure.
	}
	m, err := readFile()
	if err != nil {
		return "", err
	}
	return m[name], nil
}

func Delete(name string) error {
	var krErr error
	if useKeyring() {
		if err := keyring.Delete(service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			krErr = err
		}
	}
	m, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		// The file store needed no cleanup: a real keyring failure is an
		// error rather than a silent success. ErrNotFound already counts as
		// deleted above.
		return krErr
	}
	delete(m, name)
	return writeFile(m)
}
