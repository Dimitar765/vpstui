package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"vpstui/internal/apitypes"
	"vpstui/internal/store"
)

// registerHooks: inbound deploy webhooks. Public by design — the token in
// the path is the credential, and optional HMAC verification binds it to the
// app's webhook_secret.
func (s *Server) registerHooks(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/hooks/{token}", s.handleHook)
}

// hookPayload is the permissive shape all three providers parse into. Only
// the branch really matters; everything else is ignored.
type hookPayload struct {
	Ref        string `json:"ref"`
	Branch     string `json:"branch"`
	ObjectKind string `json:"object_kind"`
}

// hookBranch extracts the pushed branch:
//   - GitHub: X-GitHub-Event header present, ref like "refs/heads/main";
//   - GitLab: object_kind "push", ref is the bare branch;
//   - generic: {"branch": "…"} (falls back to ref).
func hookBranch(r *http.Request, body []byte) string {
	var p hookPayload
	_ = json.Unmarshal(body, &p)
	branch := p.Branch
	if branch == "" && p.Ref != "" {
		branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	}
	if r.Header.Get("X-GitHub-Event") != "" && p.Ref != "" {
		branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	}
	return branch
}

// hookVerify checks the optional HMAC-SHA256 signature against the app's
// source_config.webhook_secret. GitHub signs the raw body into
// X-Hub-Signature-256 ("sha256=<hex>"); generic providers may pass ?sig=<hex>.
// No secret configured → everything passes.
func hookVerify(r *http.Request, body []byte, secret string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		got, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sig), "sha256="))
		return err == nil && hmac.Equal(got, want)
	}
	if sig := r.URL.Query().Get("sig"); sig != "" {
		got, err := hex.DecodeString(strings.TrimSpace(sig))
		return err == nil && hmac.Equal(got, want)
	}
	return false
}

// appBranch reads the app's deploy branch from source_config, defaulting to
// main like the deploy engine's dockerfile source.
func appBranch(cfg map[string]any) string {
	if b, ok := cfg["branch"].(string); ok && strings.TrimSpace(b) != "" {
		return strings.TrimSpace(b)
	}
	return "main"
}

func webhookSecret(cfg map[string]any) string {
	s, _ := cfg["webhook_secret"].(string)
	return s
}

// handleHook enqueues a deployment for the token's app:
// verified + branch match + auto_deploy → 202 {"deploymentId":…}, else 204.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	app, err := s.db.AppByWebhookToken(r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown webhook token")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if !hookVerify(r, body, webhookSecret(app.SourceConfig)) {
		writeErr(w, http.StatusForbidden, "signature verification failed")
		return
	}
	if !app.AutoDeploy {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if hookBranch(r, body) != appBranch(app.SourceConfig) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	depID, err := s.engine.Enqueue(app.ID, apitypes.TriggerWebhook)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, deployResp{DeploymentID: depID})
}
