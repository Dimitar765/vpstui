package deploy

import (
	"strings"
	"testing"
	"time"
)

func TestTraefikLabels(t *testing.T) {
	got := TraefikLabels([]string{"a.example.com", "b.example.com"}, 3000, "abc123", "ops@example.com")
	want := map[string]string{
		"traefik.enable":                                          "true",
		"traefik.http.routers.abc123.rule":                        "Host(`a.example.com`)",
		"traefik.http.routers.abc123.entrypoints":                 "websecure",
		"traefik.http.routers.abc123.tls.certresolver":            "le",
		"traefik.http.services.abc123.loadbalancer.server.port":   "3000",
		"traefik.http.routers.abc123-1.rule":                      "Host(`b.example.com`)",
		"traefik.http.routers.abc123-1.entrypoints":               "websecure",
		"traefik.http.routers.abc123-1.tls.certresolver":          "le",
		"traefik.http.services.abc123-1.loadbalancer.server.port": "3000",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if TraefikLabels(nil, 80, "x", "e@x.com") != nil {
		t.Error("no domains should produce no labels")
	}
	if TraefikLabels([]string{"d"}, 0, "x", "e@x.com") != nil {
		t.Error("no port should produce no labels")
	}

	// Without an ACME email: web entrypoint, no TLS keys at all.
	got = TraefikLabels([]string{"a.example.com"}, 3000, "abc123", "")
	want = map[string]string{
		"traefik.enable":                                        "true",
		"traefik.http.routers.abc123.rule":                      "Host(`a.example.com`)",
		"traefik.http.routers.abc123.entrypoints":               "web",
		"traefik.http.services.abc123.loadbalancer.server.port": "3000",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRenderComposeSnapshot(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	imageApp := testApp("aa11", "image", map[string]any{"image": "docker.io/library/nginx:1.27-alpine"})
	imageApp.UpdatedAt = base
	got, err := RenderCompose(imageApp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := `services:
  app:
    image: 'docker.io/library/nginx:1.27-alpine'
    restart: unless-stopped
    env_file:
      - .env
    labels:
      traefik.enable: 'true'
      traefik.http.routers.aa11.entrypoints: 'web'
      traefik.http.routers.aa11.rule: 'Host(` + "`demo.example.com`)'" + `
      traefik.http.services.aa11.loadbalancer.server.port: '80'
    networks:
      - vpstui
networks:
  vpstui:
    external: true
`
	if string(got) != want {
		for i := range len(got) {
			if i >= len(want) || got[i] != want[i] {
				t.Fatalf("first diff at %d: got %q want %q\nGOT:\n%s", i,
					got[max(0, i-20):min(len(got), i+20)],
					want[max(0, i-20):min(len(want), i+20)], got)
			}
		}
		t.Fatalf("length differs: got %d want %d\n%s", len(got), len(want), got)
	}

	dfApp := testApp("bb22", "dockerfile", map[string]any{"dockerfile": "deploy.Dockerfile"})
	got, err = RenderCompose(dfApp, "./repo", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"build:", "context: ./repo", "dockerfile: 'deploy.Dockerfile'"} {
		if !strings.Contains(string(got), frag) {
			t.Errorf("dockerfile compose missing %q:\n%s", frag, got)
		}
	}

	stApp := testApp("cc33", "static", nil)
	got, err = RenderCompose(stApp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "image: 'nginx:alpine'") ||
		!strings.Contains(string(got), "./public:/usr/share/nginx/html:ro") {
		t.Fatalf("static compose wrong:\n%s", got)
	}

	tplApp := testApp("dd44", "template", map[string]any{
		"image": "louislam/uptime-kuma:1", "volumes": []any{"dd44-data:/app/data"},
	})
	tplApp.Domains = nil
	tplApp.Port = 0
	got, err = RenderCompose(tplApp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "image: 'louislam/uptime-kuma:1'") ||
		!strings.Contains(string(got), "- 'dd44-data:/app/data'") {
		t.Fatalf("template compose wrong:\n%s", got)
	}
	if strings.Contains(string(got), "traefik") {
		t.Fatalf("template without domains must have no traefik labels:\n%s", got)
	}

	if _, err := RenderCompose(testApp("ee55", "image", map[string]any{}), "", ""); err == nil {
		t.Fatal("image source without image must fail")
	}
}

func TestEnvFile(t *testing.T) {
	got := string(envFile(map[string]string{"B": "2", "A": "1"}))
	if got != "A=1\nB=2\n" {
		t.Fatalf("env file = %q", got)
	}
}
