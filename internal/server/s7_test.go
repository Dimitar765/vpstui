package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/store"
)

// ---------------------------------------------------------------- bootstrap

// s7env extends the shared env with an authenticated admin token.
type s7env struct {
	*env
	tok    string
	booted bool
}

func newS7Env(t *testing.T) *s7env {
	e := newEnv(t)
	res, body := e.do("POST", "/api/setup", "", map[string]string{
		"token": e.token, "email": "a@b.c", "password": "hunter22"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d %v", res.StatusCode, body)
	}
	res, body = e.do("POST", "/api/auth/login", "", map[string]string{"email": "a@b.c", "password": "hunter22"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %v", res.StatusCode, body)
	}
	tok, _ := body["token"].(string)
	return &s7env{env: e, tok: tok}
}

// appFixture inserts the project/environment/server rows once, then one app
// row per call.
func (e *s7env) appFixture(t *testing.T, token string, autoDeploy bool, cfg map[string]any) store.App {
	t.Helper()
	if !e.booted {
		if err := e.db.CreateProject(store.Project{ID: "p1", Name: "proj", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if err := e.db.CreateEnvironment(store.Environment{ID: "e1", ProjectID: "p1", Name: "prod"}); err != nil {
			t.Fatal(err)
		}
		if err := e.db.CreateServer(store.Server{ID: "s1", Name: "loop", Address: "127.0.0.1", Port: 1,
			User: "u", AuthType: "key", BaseDir: "/tmp/vpstui-test", MonitorIntervalSec: 15,
			ProxyHTTPPort: 8080, ProxyHTTPSPort: 8443, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		e.booted = true
	}
	app := store.App{ID: "app-" + token, EnvironmentID: "e1", ServerID: "s1", Name: "web-" + token,
		SourceType: "image", SourceConfig: cfg, EnvVars: map[string]string{},
		Domains: []string{}, Port: 80, AutoDeploy: autoDeploy, WebhookToken: token,
		Status: apitypes.AppUndeployed, UpdatedAt: time.Now().UTC()}
	if err := e.db.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	return app
}

// rawDo issues a request with full control over headers, query and body.
func (e *s7env) rawDo(t *testing.T, method, path string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.api.URL+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// ---------------------------------------------------------------- webhooks

func TestHookGeneric(t *testing.T) {
	e := newS7Env(t)
	app := e.appFixture(t, "wh_tok1", true, map[string]any{"branch": "main"})

	// Unknown token.
	if res := e.rawDo(t, "POST", "/api/hooks/nope", nil, []byte(`{"branch":"main"}`)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token: %d", res.StatusCode)
	}
	// Branch miss → 204.
	if res := e.rawDo(t, "POST", "/api/hooks/wh_tok1", nil, []byte(`{"branch":"develop"}`)); res.StatusCode != http.StatusNoContent {
		t.Fatalf("branch miss: %d", res.StatusCode)
	}
	// auto_deploy off → 204 (before the branch check per spec).
	e.appFixture(t, "wh_tok2", false, map[string]any{"branch": "main"})
	if res := e.rawDo(t, "POST", "/api/hooks/wh_tok2", nil, []byte(`{"branch":"main"}`)); res.StatusCode != http.StatusNoContent {
		t.Fatalf("auto_deploy off: %d", res.StatusCode)
	}

	// Matching branch → 202 + deploymentId. The engine queue runs for real:
	// it dials 127.0.0.1:1 (refused) and marks the deployment failed — the
	// webhook contract only cares about the enqueue.
	e.srv.engine.Start()
	t.Cleanup(e.srv.engine.Stop)
	res, body := e.doAny("POST", "/api/hooks/wh_tok1", "", map[string]string{"branch": "main"})
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("hook: %d %v", res.StatusCode, body)
	}
	m := body.(map[string]any)
	depID, _ := m["deploymentId"].(string)
	if depID == "" {
		t.Fatalf("no deploymentId: %v", m)
	}
	dep, err := e.db.DeploymentByID(depID)
	if err != nil {
		t.Fatal(err)
	}
	if dep.Trigger != apitypes.TriggerWebhook || dep.AppID != app.ID {
		t.Fatalf("deployment = %+v", dep)
	}
}

func TestHookProvidersAndHMAC(t *testing.T) {
	e := newS7Env(t)
	const secret = "hsec"
	e.appFixture(t, "wh_gh", true, map[string]any{"branch": "main", "webhook_secret": secret})
	body := []byte(`{"ref":"refs/heads/main","repository":{"name":"web"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	// Missing signature with secret set → 403.
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gh", map[string]string{"X-GitHub-Event": "push"}, body); res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing sig: %d", res.StatusCode)
	}
	// Wrong signature → 403.
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gh",
		map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=deadbeef"}, body); res.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong sig: %d", res.StatusCode)
	}
	// GitHub signature accepted (queue not started → 500 surfaces the
	// enqueue error; use it as the "verified" sentinel).
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gh",
		map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sig}, body); res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("github sig verify: %d", res.StatusCode)
	}
	// Generic ?sig= accepted.
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gh?sig="+sig, nil, body); res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("generic sig verify: %d", res.StatusCode)
	}
	// GitLab-style push with secret-free app and branch match → 500 (enqueue
	// w/o queue) proves verification+branch passed.
	e.appFixture(t, "wh_gl", true, map[string]any{})
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gl", nil, []byte(`{"object_kind":"push","ref":"main"}`)); res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("gitlab branch match: %d", res.StatusCode)
	}
	// GitLab branch miss → 204.
	if res := e.rawDo(t, "POST", "/api/hooks/wh_gl", nil, []byte(`{"object_kind":"push","ref":"dev"}`)); res.StatusCode != http.StatusNoContent {
		t.Fatalf("gitlab branch miss: %d", res.StatusCode)
	}
}

// ---------------------------------------------------------------- password

func TestAuthPasswordFlow(t *testing.T) {
	e := newS7Env(t)

	// Wrong current password → 403.
	res, _ := e.do("POST", "/api/auth/password", e.tok, map[string]string{"current": "wrong", "next": "newpassword1"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong current: %d", res.StatusCode)
	}
	// Short next → 400.
	res, _ = e.do("POST", "/api/auth/password", e.tok, map[string]string{"current": "hunter22", "next": "short"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("short next: %d", res.StatusCode)
	}
	// Unauthenticated → 401.
	res, _ = e.do("POST", "/api/auth/password", "", map[string]string{"current": "hunter22", "next": "newpassword1"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth: %d", res.StatusCode)
	}
	// Success.
	res, _ = e.do("POST", "/api/auth/password", e.tok, map[string]string{"current": "hunter22", "next": "newpassword1"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change: %d", res.StatusCode)
	}
	// Existing sessions stay valid.
	res, _ = e.do("GET", "/api/me", e.tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("old session invalidated: %d", res.StatusCode)
	}
	// Re-login with the new password works; the old one is dead.
	res, _ = e.do("POST", "/api/auth/login", "", map[string]string{"email": "a@b.c", "password": "newpassword1"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-login: %d", res.StatusCode)
	}
	res, _ = e.do("POST", "/api/auth/login", "", map[string]string{"email": "a@b.c", "password": "hunter22"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", res.StatusCode)
	}
}

// ---------------------------------------------------------------- alerts CRUD

func TestAlertCRUDValidation(t *testing.T) {
	e := newS7Env(t)

	// Bad metric.
	res, _ := e.do("POST", "/api/alerts", e.tok, map[string]any{"metric": "cpu", "threshold": 1})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad metric: %d", res.StatusCode)
	}
	// Bad regexp.
	res, _ = e.do("POST", "/api/alerts", e.tok, map[string]any{"metric": "container_down", "containerPattern": "(", "threshold": 0})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad regexp: %d", res.StatusCode)
	}
	// Pattern on a host metric → 400.
	res, _ = e.do("POST", "/api/alerts", e.tok, map[string]any{"metric": "load1", "containerPattern": "web"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("pattern on host metric: %d", res.StatusCode)
	}
	// Unknown server.
	res, _ = e.do("POST", "/api/alerts", e.tok, map[string]any{"metric": "load1", "serverId": "ghost"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown server: %d", res.StatusCode)
	}
	// Valid create with defaults.
	res, rule := e.do("POST", "/api/alerts", e.tok, map[string]any{"metric": "load1", "threshold": 0.7})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", res.StatusCode, rule)
	}
	if rule["state"] != apitypes.AlertStateOK || rule["durationSec"].(float64) != 300 || rule["enabled"] != true {
		t.Fatalf("defaults wrong: %v", rule)
	}
	id, _ := rule["id"].(string)

	// Update threshold.
	res, body := e.do("PUT", "/api/alerts/"+id, e.tok, map[string]any{"metric": "load1", "threshold": 0.9, "durationSec": 0})
	if res.StatusCode != http.StatusOK || body["threshold"].(float64) != 0.9 {
		t.Fatalf("update: %d %v", res.StatusCode, body)
	}
	// List shows one.
	res, list := e.doAny("GET", "/api/alerts", e.tok, nil)
	if res.StatusCode != http.StatusOK || len(list.([]any)) != 1 {
		t.Fatalf("list: %d %v", res.StatusCode, list)
	}
	// Delete.
	if res, _ = e.do("DELETE", "/api/alerts/"+id, e.tok, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	// Second delete → 404.
	if res, _ = e.do("DELETE", "/api/alerts/"+id, e.tok, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-delete: %d", res.StatusCode)
	}
}

// ---------------------------------------------------------------- notifications

func TestNotificationCRUDAndTestEndpoint(t *testing.T) {
	e := newS7Env(t)

	// Bad kind.
	res, _ := e.do("POST", "/api/notifications", e.tok, map[string]any{"kind": "sms", "config": map[string]any{}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", res.StatusCode)
	}
	// Missing required config key.
	res, _ = e.do("POST", "/api/notifications", e.tok, map[string]any{"kind": "discord", "config": map[string]any{}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing webhook_url: %d", res.StatusCode)
	}
	// Unknown event.
	res, _ = e.do("POST", "/api/notifications", e.tok, map[string]any{"kind": "slack",
		"config": map[string]any{"webhook_url": "http://127.0.0.1:1/x"}, "events": []string{"deployexploded"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown event: %d", res.StatusCode)
	}

	// Valid webhook channel pointed at a local receiver.
	var mu sync.Mutex
	var got map[string]any
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got = m
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(rcv.Close)
	res, body := e.do("POST", "/api/notifications", e.tok, map[string]any{
		"kind": "webhook", "config": map[string]any{"url": rcv.URL},
		"events": []string{apitypes.EventAlertFired, apitypes.EventDeployFailed}})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", res.StatusCode, body)
	}
	ch := body
	id, _ := ch["id"].(string)

	// Test endpoint delivers a test payload synchronously.
	res, body = e.do("POST", "/api/notifications/"+id+"/test", e.tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("test endpoint: %d %v", res.StatusCode, body)
	}
	mu.Lock()
	if got == nil || got["event"] != apitypes.EventTest {
		mu.Unlock()
		t.Fatalf("receiver got %v", got)
	}
	mu.Unlock()

	// List + delete.
	res, list := e.doAny("GET", "/api/notifications", e.tok, nil)
	if res.StatusCode != http.StatusOK || len(list.([]any)) != 1 {
		t.Fatalf("list: %d %v", res.StatusCode, list)
	}
	if res, _ = e.do("DELETE", "/api/notifications/"+id, e.tok, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res, _ = e.do("POST", "/api/notifications/"+id+"/test", e.tok, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("test after delete: %d", res.StatusCode)
	}
}
