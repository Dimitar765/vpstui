package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vpstui/internal/config"
	"vpstui/internal/models"
	"vpstui/internal/store"
)

const (
	setupTokenTTL = 15 * time.Minute
	sessionTTL    = 30 * 24 * time.Hour
)

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------- setup

// EnsureSetupToken generates the first-boot setup token when no admin
// exists: printed to stdout and stored at <data>/setup-token (0600, atomic).
// It expires after 15 minutes; restarting with an empty users table makes a
// fresh one. An existing token file is refreshed so the on-disk copy is the
// live token.
func (s *Server) EnsureSetupToken() error {
	n, err := s.db.CountUsers()
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	tok, err := randomHex(24)
	if err != nil {
		return err
	}
	s.setupTok = tok
	if s.cfg.DataDir != "" {
		if err := config.WriteFileAtomic(s.cfg.DataDir+"/setup-token", []byte(tok+"\n"), 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("vpstui-server setup token (valid 15 minutes): %s\n", tok)
	return nil
}

type setupReq struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if s.setupTok == "" || req.Token != s.setupTok || time.Since(s.setupAt()) > setupTokenTTL {
		writeErr(w, http.StatusUnauthorized, "invalid or expired setup token")
		return
	}
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	n, err := s.db.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "admin already exists")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.CreateUser(models.NewID(), req.Email, string(hash)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setupTok = ""
	_ = os.Remove(s.cfg.DataDir + "/setup-token")
	writeJSON(w, http.StatusOK, map[string]string{"status": "created"})
}

// setupAt reads the token file's mtime; zero when there is no file. With no
// file (DataDir empty / unit tests) the in-memory token never ages out.
func (s *Server) setupAt() time.Time {
	if s.cfg.DataDir == "" {
		return time.Now()
	}
	fi, err := os.Stat(s.cfg.DataDir + "/setup-token")
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// ---------------------------------------------------------------- login

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	u, err := s.db.UserByEmail(req.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	tok, err := randomHex(32)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.CreateSession(hashToken(tok), u.ID, time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearer(r); tok != "" {
		_ = s.db.DeleteSession(hashToken(tok))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- password

// handlePasswordChange rotates the admin password: verifies the current one
// (403 on mismatch), enforces the setup-time minimum length, and keeps every
// existing session valid — only the stored hash changes.
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.UserByID(userID(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Current)) != nil {
		writeErr(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if len(req.Next) < 8 {
		writeErr(w, http.StatusBadRequest, "next password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Next), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.UpdateUserPassword(u.ID, string(hash)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- me / keys

type meResp struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.UserByID(userID(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, meResp{ID: u.ID, Email: u.Email})
}

type keyResp struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Token      string     `json:"token,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func (s *Server) handleKeysCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	raw, err := randomHex(16)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := "vps_" + raw
	id := models.NewID()
	if err := s.db.CreateKey(id, strings.TrimSpace(req.Name), hashToken(tok)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The raw token is returned exactly once; only its hash is stored.
	writeJSON(w, http.StatusCreated, keyResp{ID: id, Name: req.Name, Token: tok, CreatedAt: time.Now()})
}

func (s *Server) handleKeysList(w http.ResponseWriter, r *http.Request) {
	keys, err := s.db.Keys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]keyResp, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyResp{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleKeysDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteKey(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "key not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------- auth middleware

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

type ctxKey int

const ctxUser ctxKey = 1

func userID(r *http.Request) string {
	v, _ := r.Context().Value(ctxUser).(string)
	return v
}

// authMW resolves the bearer token — a session token or an API key — and
// attaches the user id. Healthz, setup and login pass through
// unauthenticated, as do the deploy webhooks: their credential is the
// unguessable token in the path plus the optional HMAC check.
func (s *Server) authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/setup" || r.URL.Path == "/api/auth/login" ||
			strings.HasPrefix(r.URL.Path, "/api/hooks/") ||
			!strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		tok := bearer(r)
		if tok == "" {
			// WS endpoints (/api/ws, /api/deployments/{id}/logs) can't set
			// headers in the browser API — they pass the bearer as ?token=.
			tok = r.URL.Query().Get("token")
		}
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		hash := hashToken(tok)
		if sess, err := s.db.Session(hash); err == nil {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, sess.UserID)))
			return
		}
		if k, err := s.db.KeyByTokenHash(hash); err == nil {
			_ = s.db.TouchKey(k.ID) // at most once a minute inside
			// API keys act for the single admin (users/teams deferred).
			u, err := s.db.AdminUser()
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u.ID)))
			return
		}
		writeErr(w, http.StatusUnauthorized, "unauthorized")
	})
}
