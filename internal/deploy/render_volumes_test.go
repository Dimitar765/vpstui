package deploy

import (
	"strings"
	"testing"
)

func TestRenderComposeTemplateVolumesIDSubstitution(t *testing.T) {
	// {{id}} in volumes must become the APP id (deliberate deviation from
	// the spec's deployment id: keeps data across redeploys), and named
	// volumes must be declared in a top-level volumes block.
	app := testApp("ab12", "template", map[string]any{
		"image": "louislam/uptime-kuma:1",
		"volumes": []any{
			"{{id}}-data:/app/data",
			"/host/persist:/etc/x", // bind mount: no top-level declaration
		},
	})
	got, err := RenderCompose(app, "", "")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(got)
	if !strings.Contains(doc, "- 'ab12-data:/app/data'") {
		t.Fatalf("service mount not substituted:\n%s", doc)
	}
	if !strings.Contains(doc, "volumes:\n  ab12-data: {}\n") {
		t.Fatalf("top-level named volume missing:\n%s", doc)
	}
	if strings.Contains(doc, "\n  /host/persist: {}\n") {
		t.Fatal("bind mount must not be declared as a named volume")
	}
	// Bind mount still present as a service mount, absent from the top block.
	if !strings.Contains(doc, "- '/host/persist:/etc/x'") {
		t.Fatalf("bind mount missing:\n%s", doc)
	}
	if strings.Count(doc, "\nvolumes:\n") != 1 {
		t.Fatalf("expected exactly one top-level volumes block:\n%s", doc)
	}
}

func TestRenderComposeTemplateComposeType(t *testing.T) {
	app := testApp("cd34", "template", map[string]any{
		"compose_yaml": `
services:
  web:
    image: ghcr.io/example/web:latest
    environment:
      DB: sqlite:///{{id}}-db/app.db
  db:
    image: postgres:16-alpine
    volumes:
      - "{{id}}-db:/var/lib/postgresql/data"
`,
		"public_service": "web",
	})
	app.Domains = []string{"shop.example.com"}
	app.Port = 8080
	got, err := RenderCompose(app, "", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(got)
	for _, frag := range []string{
		"cd34-db:/var/lib/postgresql/data",  // {{id}} substituted
		"cd34-db: {}",                       // named volume declared
		"image: ghcr.io/example/web:latest", // services preserved
		"traefik.enable:",                   // labels merged on public svc
		"Host(`shop.example.com`)",
		`loadbalancer.server.port: "8080"`,
		"vpstui:",        // network declared external
		"cd34-db/app.db", // substitution inside env values
	} {
		if !strings.Contains(doc, frag) {
			t.Fatalf("compose template output missing %q:\n%s", frag, doc)
		}
	}
}

func TestRenderComposeTemplateComposeUnknownService(t *testing.T) {
	app := testApp("ee55", "template", map[string]any{
		"compose_yaml":   "services:\n  web:\n    image: x\n",
		"public_service": "wrong",
	})
	if _, err := RenderCompose(app, "", ""); err == nil {
		t.Fatal("unknown public_service must fail")
	}
}
