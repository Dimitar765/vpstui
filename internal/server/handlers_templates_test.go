package server

import (
	"net/http"
	"testing"

	"vpstui/internal/templates"
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

// TestAppsCreateTemplateResolves covers server-side template resolution on
// POST/PUT /api/apps: the GUI wizard sends sourceConfig={template_id} only,
// so the API must fill in image/compose_yaml/port before storing the row —
// otherwise RenderCompose fails with "source_config.image is required".
func TestAppsCreateTemplateResolves(t *testing.T) {
	e := newEnv(t)
	res, _ := e.do("POST", "/api/setup", "", map[string]string{"token": e.token, "email": "a@b.c", "password": "hunter22"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}
	_, m := e.do("POST", "/api/auth/login", "", map[string]string{"email": "a@b.c", "password": "hunter22"})
	tok, _ := m["token"].(string)

	res, m = e.do("POST", "/api/servers", tok, map[string]any{
		"name": "loop", "address": "127.0.0.1", "port": 2222, "user": "u",
		"authType": "key", "keyPath": "/dev/null", "baseDir": "/opt/vpstui",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("server create: %d", res.StatusCode)
	}
	srvID, _ := m["id"].(string)
	_, m = e.do("POST", "/api/projects", tok, map[string]string{"name": "P"})
	projID, _ := m["id"].(string)
	res, m = e.do("POST", "/api/projects/"+projID+"/environments", tok, map[string]string{"name": "production"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("environment create: %d %v", res.StatusCode, m)
	}
	envID, _ := m["id"].(string)

	kuma, err := templates.Get("uptime-kuma")
	if err != nil {
		t.Fatal(err)
	}

	// Image template: the request carries only template_id; the stored row
	// must carry the resolved catalog config.
	res, m = e.do("POST", "/api/apps", tok, map[string]any{
		"environmentId": envID, "serverId": srvID, "name": "kuma",
		"sourceType": "template", "sourceConfig": map[string]string{"template_id": "uptime-kuma"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("template app create: %d %v", res.StatusCode, m)
	}
	appID, _ := m["id"].(string)
	res, m = e.do("GET", "/api/apps/"+appID, tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app get: %d", res.StatusCode)
	}
	if m["port"] != float64(kuma.Port) {
		t.Fatalf("port not resolved from template: %v, want %d", m["port"], kuma.Port)
	}
	cfg, _ := m["sourceConfig"].(map[string]any)
	if cfg["template_id"] != "uptime-kuma" || cfg["image"] != kuma.Image {
		t.Fatalf("source_config not resolved: %v", cfg)
	}

	// Compose template: compose_yaml/public_service resolved, no image key.
	var composeID string
	for _, tp := range templates.List() {
		if tp.Type == "compose" {
			composeID = tp.ID
			break
		}
	}
	if composeID == "" {
		t.Fatal("no compose template in catalog")
	}
	ctpl, err := templates.Get(composeID)
	if err != nil {
		t.Fatal(err)
	}
	res, m = e.do("POST", "/api/apps", tok, map[string]any{
		"environmentId": envID, "serverId": srvID, "name": "comp",
		"sourceType": "template", "sourceConfig": map[string]string{"template_id": composeID},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("compose app create: %d %v", res.StatusCode, m)
	}
	cfg, _ = m["sourceConfig"].(map[string]any)
	y, _ := cfg["compose_yaml"].(string)
	ps, _ := cfg["public_service"].(string)
	if y == "" || ps == "" {
		t.Fatalf("compose config not resolved: %v", cfg)
	}
	if _, ok := cfg["image"]; ok {
		t.Fatalf("compose app must not carry image: %v", cfg)
	}
	if m["port"] != float64(ctpl.Port) {
		t.Fatalf("compose port wrong: %v, want %d", m["port"], ctpl.Port)
	}

	// Error contract: missing template_id and unknown id both 400.
	res, m = e.do("POST", "/api/apps", tok, map[string]any{
		"environmentId": envID, "serverId": srvID, "name": "bad",
		"sourceType": "template", "sourceConfig": map[string]string{},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing template_id: %d %v", res.StatusCode, m)
	}
	res, m = e.do("POST", "/api/apps", tok, map[string]any{
		"environmentId": envID, "serverId": srvID, "name": "bad2",
		"sourceType": "template", "sourceConfig": map[string]string{"template_id": "nope"},
	})
	if res.StatusCode != http.StatusBadRequest || m["error"] != "template not found" {
		t.Fatalf("unknown template_id: %d %v", res.StatusCode, m)
	}

	// Update heal: a stripped {template_id} cfg PUT back re-resolves.
	res, m = e.do("PUT", "/api/apps/"+appID, tok, map[string]any{
		"sourceType": "template", "sourceConfig": map[string]string{"template_id": "uptime-kuma"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update heal: %d %v", res.StatusCode, m)
	}
	res, m = e.do("GET", "/api/apps/"+appID, tok, nil)
	cfg, _ = m["sourceConfig"].(map[string]any)
	if cfg["image"] != kuma.Image || m["port"] != float64(kuma.Port) {
		t.Fatalf("update did not heal source_config: %v (port %v)", cfg, m["port"])
	}
}
