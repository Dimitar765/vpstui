package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Settings is the user's TUI preferences, persisted at
// ~/.config/vpstui/settings.json. Every field has a built-in default; the
// resolution order is environment variable > settings file > default, which
// the loader realises by returning the file values and letting the caller
// keep the environment authoritative for the env-backed fields (VPSTUI_*).
type Settings struct {
	Theme    string `json:"theme"`    // dark | light | high-contrast
	LogTail  int    `json:"logTail"`  // docker logs --tail for live streams
	ErrSince string `json:"errSince"` // error-sweep window, Go duration
	ErrTail  int    `json:"errTail"`  // error-sweep per-container tail
	HTTPAddr string `json:"httpAddr"` // loopback bind address of the JSON API
	Mouse    *bool  `json:"mouse"`    // nil = default true
	LogWrap  bool   `json:"logWrap"`  // true: soft-wrap long log lines
}

// MouseEnabled reports whether mouse reporting should be on; the pointer form
// keeps "absent field" distinct from an explicit false.
func (s Settings) MouseEnabled() bool {
	return s.Mouse == nil || *s.Mouse
}

const (
	defaultTheme   = "dark"
	defaultLogTail = 2000
	defaultErrTail = 200
)

// DefaultSettings mirrors the built-ins the TUI shipped with before this file
// existed. Env-backed fields carry their empty value here: the TUI treats an
// empty file value as "no preference" so the environment can still decide.
func DefaultSettings() Settings {
	return Settings{
		Theme:    defaultTheme,
		LogTail:  defaultLogTail,
		ErrSince: "",
		ErrTail:  defaultErrTail,
		HTTPAddr: "",
		Mouse:    nil,
		LogWrap:  false,
	}
}

func settingsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// LoadSettings reads settings.json. A missing file yields the defaults with no
// error — a fresh install is not a problem. A corrupt file is renamed aside to
// settings.json.bad (the same loader pattern hosts.json uses) and the defaults
// are returned with the error, so the caller can warn and carry on.
func LoadSettings() (Settings, error) {
	p, err := settingsPath()
	if err != nil {
		return DefaultSettings(), err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return DefaultSettings(), err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		renameAside(p)
		def := DefaultSettings()
		return def, fmt.Errorf("corrupt settings file %s — renamed to %s.bad: %w", p, p, err)
	}
	return s.normalized(), nil
}

// normalized fills absent/invalid fields with their defaults, so the rest of
// the program never sees a zero value that means "unset".
func (s Settings) normalized() Settings {
	def := DefaultSettings()
	if s.Theme == "" {
		s.Theme = def.Theme
	}
	if s.LogTail <= 0 {
		s.LogTail = def.LogTail
	}
	if s.ErrTail <= 0 {
		s.ErrTail = def.ErrTail
	}
	return s
}

// SaveSettings stores the settings atomically, the same way hosts.json is
// stored.
func SaveSettings(s Settings) error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p, append(data, '\n'), 0600)
}

// EnvDefaults injects the file's env-backed fields into the environment for
// any VPSTUI_* variable the user has not set, realising the resolution order
// env > file > default with one boring mechanism: the existing env readers in
// the TUI and the API server keep being the single source of truth. Called
// once at startup, before any of those readers run.
func EnvDefaults(s Settings) {
	setEnvDefault("VPSTUI_LOG_TAIL", itoaPos(s.LogTail))
	setEnvDefault("VPSTUI_ERR_TAIL", itoaPos(s.ErrTail))
	setEnvDefault("VPSTUI_ERR_SINCE", s.ErrSince)
	setEnvDefault("VPSTUI_HTTP_ADDR", s.HTTPAddr)
}

func itoaPos(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func setEnvDefault(key, val string) {
	if val == "" {
		return
	}
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, val)
	}
}
