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

const service = "vpstui"

var (
	mu        sync.Mutex
	keyringOK *bool // lazily probed; nil = unknown
)

func useKeyring() bool {
	mu.Lock()
	defer mu.Unlock()
	if keyringOK == nil {
		ok := keyring.Set(service, "__probe__", "1") == nil
		if ok {
			_ = keyring.Delete(service, "__probe__")
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
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
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
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		// other keyring errors fall through to the file store
	}
	m, err := readFile()
	if err != nil {
		return "", err
	}
	return m[name], nil
}

func Delete(name string) error {
	if useKeyring() {
		_ = keyring.Delete(service, name)
	}
	m, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return writeFile(m)
}
