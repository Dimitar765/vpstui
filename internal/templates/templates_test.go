package templates

import (
	"strings"
	"testing"
)

func TestCatalogComplete(t *testing.T) {
	want := []string{
		"uptime-kuma", "n8n", "grafana", "vaultwarden",
		"mailpit", "dozzle", "plausible", "umami",
	}
	list := List()
	if len(list) != len(want) {
		t.Fatalf("catalog has %d entries, want %d", len(list), len(want))
	}
	seen := map[string]bool{}
	for _, tpl := range list {
		seen[tpl.ID] = true
		if tpl.Name == "" || tpl.Description == "" || tpl.Category == "" || tpl.Logo == "" {
			t.Errorf("%s: missing name/description/category/logo", tpl.ID)
		}
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("catalog missing %q", id)
		}
	}
}

func TestGetRoundTripAndUnknown(t *testing.T) {
	tpl, err := Get("uptime-kuma")
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Image != "louislam/uptime-kuma:1" || tpl.Port != 3001 ||
		len(tpl.Volumes) != 1 || tpl.Volumes[0] != "{{id}}-data:/app/data" {
		t.Fatalf("uptime-kuma entry wrong: %+v", tpl)
	}
	if _, err := Get("nope"); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestImageTemplateApp(t *testing.T) {
	tpl, _ := Get("grafana")
	a := tpl.App("srv1", "env1", "", []string{"g.example.com"})
	if a.SourceType != "template" || a.ServerID != "srv1" || a.EnvironmentID != "env1" ||
		a.Name != "Grafana" || a.Port != 3000 {
		t.Fatalf("app row wrong: %+v", a)
	}
	if a.SourceConfig["image"] != "grafana/grafana:latest" || a.SourceConfig["template_id"] != "grafana" {
		t.Fatalf("source_config wrong: %v", a.SourceConfig)
	}
	vols, ok := a.SourceConfig["volumes"].([]any)
	if !ok || len(vols) != 1 || vols[0] != "{{id}}-data:/var/lib/grafana" {
		t.Fatalf("volumes wrong: %v", a.SourceConfig["volumes"])
	}

	// Compose template: compose_yaml + public_service, no image key.
	c, err := Get("plausible")
	if err != nil {
		t.Fatal(err)
	}
	ca := c.App("srv1", "env1", "", nil)
	if ca.SourceConfig["compose_yaml"] == nil || ca.SourceConfig["public_service"] != "plausible" {
		t.Fatalf("compose source_config wrong: %v", ca.SourceConfig)
	}
	if _, ok := ca.SourceConfig["image"]; ok {
		t.Fatal("compose template must not carry image")
	}
}

func TestComposeYAMLPlaceholders(t *testing.T) {
	for _, id := range []string{"plausible", "umami"} {
		tpl, err := Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(tpl.ComposeYAML, "{{id}}-") {
			t.Errorf("%s: compose lacks {{id}} volume placeholder", id)
		}
		if strings.Count(tpl.ComposeYAML, "services:") != 1 {
			t.Errorf("%s: compose missing services block", id)
		}
	}
}
