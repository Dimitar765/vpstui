package server

import (
	"net/http"
	"testing"
)

// TestTemplatesListAndDeploy covers the catalog endpoint and deploy
// resolution: the created app row carries the template's source_config and
// the deployment is enqueued (it fails against the test target — engine
// behavior is asserted by its own tests). Also covers the null
// environmentId bootstrap: Default/production created on an empty database.
func TestTemplatesListAndDeploy(t *testing.T) {
	e := newEnv(t)
	res, _ := e.do("POST", "/api/setup", "", map[string]string{"token": e.token, "email": "a@b.c", "password": "hunter22"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}
	_, m := e.do("POST", "/api/auth/login", "", map[string]string{"email": "a@b.c", "password": "hunter22"})
	tok, _ := m["token"].(string)

	res, anyBody := e.doAny("GET", "/api/templates", tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("templates list: %d", res.StatusCode)
	}
	list, _ := anyBody.([]any)
	if len(list) != 8 {
		t.Fatalf("templates list has %d entries, want 8", len(list))
	}
	first, _ := list[0].(map[string]any)
	for _, key := range []string{"id", "name", "description", "category", "logo", "type", "port"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("template DTO missing %q: %v", key, first)
		}
	}

	res, _ = e.do("POST", "/api/servers", tok, map[string]any{
		"name": "loop", "address": "127.0.0.1", "port": 2222, "user": "u",
		"authType": "key", "keyPath": "/dev/null", "baseDir": "/opt/vpstui",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("server create: %d", res.StatusCode)
	}
	_, m = e.do("POST", "/api/servers", tok, map[string]any{
		"name": "loop2", "address": "127.0.0.1", "port": 2222, "user": "u",
		"authType": "key", "keyPath": "/dev/null", "baseDir": "/opt/vpstui",
	})
	srvID, _ := m["id"].(string)
	if srvID == "" {
		t.Fatal("server id missing")
	}
	e.srv.engine.Start() // Enqueue refuses to run against a stopped engine

	// Deploy with null environmentId on an empty DB: bootstraps Default/production.
	res, m = e.do("POST", "/api/templates/uptime-kuma/deploy", tok, map[string]any{
		"serverId": srvID, "environmentId": nil, "name": "kuma",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("template deploy: %d %v", res.StatusCode, m)
	}
	appID, _ := m["appId"].(string)
	depID, _ := m["deploymentId"].(string)
	if appID == "" || depID == "" {
		t.Fatalf("deploy response missing ids: %v", m)
	}

	a, err := e.db.AppByID(appID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "kuma" || a.SourceType != "template" || a.Port != 3001 {
		t.Fatalf("app row wrong: %+v", a)
	}
	cfg := a.SourceConfig
	if cfg["template_id"] != "uptime-kuma" || cfg["image"] != "louislam/uptime-kuma:1" {
		t.Fatalf("source_config wrong: %v", cfg)
	}
	vols, _ := cfg["volumes"].([]any)
	if len(vols) != 1 || vols[0] != "{{id}}-data:/app/data" {
		t.Fatalf("volumes wrong: %v", cfg["volumes"])
	}
	dep, err := e.db.DeploymentByID(depID)
	if err != nil {
		t.Fatal(err)
	}
	if dep.AppID != appID || dep.Trigger != "manual" {
		t.Fatalf("deployment row wrong: %+v", dep)
	}
	projects, err := e.db.Projects()
	if err != nil || len(projects) != 1 || projects[0].Name != "Default" {
		t.Fatalf("default project not bootstrapped: %v %v", projects, err)
	}

	// Unknown template id → 404.
	res, _ = e.do("POST", "/api/templates/nope/deploy", tok, map[string]any{"serverId": srvID})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown template: %d", res.StatusCode)
	}
	// Unknown server id → 400.
	res, _ = e.do("POST", "/api/templates/uptime-kuma/deploy", tok, map[string]any{"serverId": "zz"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown server: %d", res.StatusCode)
	}

	// Explicit environmentId path: reuse the bootstrapped production env.
	envs, err := e.db.EnvironmentsByProject(projects[0].ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("production env missing: %v %v", envs, err)
	}
	res, _ = e.do("POST", "/api/templates/mailpit/deploy", tok, map[string]any{
		"serverId": srvID, "environmentId": envs[0].ID, "domains": []string{"mail.example.com"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("second deploy: %d", res.StatusCode)
	}
}
