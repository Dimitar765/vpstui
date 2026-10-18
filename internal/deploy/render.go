package deploy

import (
	"fmt"
	"sort"
	"strings"

	"vpstui/internal/store"

	"gopkg.in/yaml.v3"
)

// networkName is the shared docker network every app compose joins and the
// (Phase-S4) Traefik stack watches. Created idempotently before every deploy.
const networkName = "vpstui"

// RenderCompose renders the deployment compose file for image, static,
// template and dockerfile sources. app.SourceConfig fields read:
// image (image/template), dockerfile (dockerfile build), volumes
// (template, []any of "host:container[:mode]" strings), compose_yaml +
// public_service (template of type compose). buildContext is empty for
// non-build sources, "./repo" for dockerfile.
//
// Volume strings may use the {{id}} placeholder, substituted with the APP id
// (deliberate deviation from the spec's deployment id: per-deployment volume
// names would orphan user data on every redeploy/rollback; the app id keeps
// data stable across releases, matching Dokploy/Coolify). Named volumes (no
// path separator in the host part) are declared in a top-level volumes block.
func RenderCompose(a store.App, buildContext, acmeEmail string) ([]byte, error) {
	if a.SourceType == "template" {
		if y := str(a.SourceConfig, "compose_yaml", ""); y != "" {
			return renderTemplateCompose(a, y, acmeEmail)
		}
	}
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
	}
	w("services:\n")
	w("  app:\n")
	switch a.SourceType {
	case "dockerfile":
		df := str(a.SourceConfig, "dockerfile", "Dockerfile")
		w("    build:\n      context: %s\n      dockerfile: %s\n", buildContext, shquote(df))
	default:
		img := str(a.SourceConfig, "image", "")
		if img == "" && a.SourceType == "static" {
			img = "nginx:alpine"
		}
		if img == "" {
			return nil, fmt.Errorf("app %s: source_config.image is required", a.ID)
		}
		w("    image: %s\n", shquote(img))
	}
	w("    restart: unless-stopped\n")
	w("    env_file:\n      - .env\n")
	if a.SourceType == "static" {
		w("    volumes:\n      - ./public:/usr/share/nginx/html:ro\n")
	}
	if vols := volumeList(a); len(vols) > 0 {
		w("    volumes:\n")
		for _, v := range vols {
			w("      - %s\n", shquote(v))
		}
	}
	if labels := TraefikLabels(a.Domains, a.Port, a.ID, acmeEmail); len(labels) > 0 {
		w("    labels:\n")
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w("      %s: %s\n", k, shquote(labels[k]))
		}
	}
	w("    networks:\n      - %s\n", networkName)
	w("networks:\n  %s:\n    external: true\n", networkName)
	if vols := volumeList(a); len(vols) > 0 {
		var named []string
		for _, v := range vols {
			host, _, _ := strings.Cut(v, ":")
			if host != "" && !strings.ContainsAny(host, "/.") {
				named = append(named, host)
			}
		}
		if len(named) > 0 {
			w("volumes:\n")
			for _, n := range named {
				w("  %s: {}\n", n)
			}
		}
	}
	return []byte(b.String()), nil
}

// renderTemplateCompose deploys a compose-type template: the catalog YAML
// with {{id}} → app id, vpstui's Traefik labels and network merged onto the
// public service (ours win on label conflicts, same rule as user compose).
func renderTemplateCompose(a store.App, docYAML, acmeEmail string) ([]byte, error) {
	docYAML = strings.ReplaceAll(docYAML, "{{id}}", a.ID)
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(docYAML), &doc); err != nil {
		return nil, fmt.Errorf("template compose: %w", err)
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("template compose: services block missing")
	}
	svcName := str(a.SourceConfig, "public_service", "")
	svc, ok := services[svcName].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("template compose: public service %q not found", svcName)
	}
	labels := map[string]any{}
	if m, ok := svc["labels"].(map[string]any); ok {
		labels = m
	}
	for k, v := range TraefikLabels(a.Domains, a.Port, a.ID, acmeEmail) {
		labels[k] = v
	}
	svc["labels"] = labels
	svc["networks"] = []any{networkName}
	// Collect named volumes ({{id}}-substituted, no path separators) from
	// every service mount into a top-level declaration block.
	vols, _ := doc["volumes"].(map[string]any)
	if vols == nil {
		vols = map[string]any{}
	}
	for _, raw := range services {
		sm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mounts, _ := sm["volumes"].([]any)
		for _, m := range mounts {
			s, ok := m.(string)
			if !ok {
				continue
			}
			host, _, _ := strings.Cut(s, ":")
			if host != "" && !strings.ContainsAny(host, "/.") {
				vols[host] = map[string]any{}
			}
		}
	}
	if len(vols) > 0 {
		doc["volumes"] = vols
	}
	doc["networks"] = map[string]any{networkName: map[string]any{"external": true}}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("template compose: %w", err)
	}
	return out, nil
}

// TraefikLabels builds the label map that puts the app behind the managed
// proxy: one router per domain (rule Host(`d`)), websecure entrypoint, the
// `le` cert resolver and the loadbalancer port; without an ACME email the
// routers land on the plain-HTTP `web` entrypoint instead. Router name is
// the app id — an 8-byte hex token, so it is always a valid label segment.
// Domains or a port of 0 produce no labels (app reachable only inside the
// network).
func TraefikLabels(domains []string, port int, router, acmeEmail string) map[string]string {
	if len(domains) == 0 || port == 0 {
		return nil
	}
	labels := map[string]string{"traefik.enable": "true"}
	for i, d := range domains {
		r := router
		if i > 0 {
			r = fmt.Sprintf("%s-%d", router, i)
		}
		labels["traefik.http.routers."+r+".rule"] = "Host(`" + d + "`)"
		if acmeEmail != "" {
			// TLS: websecure entrypoint + the `le` cert resolver the proxy
			// stack defines.
			labels["traefik.http.routers."+r+".entrypoints"] = "websecure"
			labels["traefik.http.routers."+r+".tls.certresolver"] = "le"
		} else {
			// No ACME email configured: plain HTTP on the web entrypoint —
			// no silent cert failures.
			labels["traefik.http.routers."+r+".entrypoints"] = "web"
		}
		labels["traefik.http.services."+r+".loadbalancer.server.port"] = fmt.Sprint(port)
	}
	return labels
}

// envFile renders the .env document written 0600 into every release: one
// KEY=value per line, deterministic order.
func envFile(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return []byte(b.String())
}

func str(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

func volumeList(a store.App) []string {
	raw, ok := a.SourceConfig["volumes"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, strings.ReplaceAll(s, "{{id}}", a.ID))
		}
	}
	return out
}

// shquote single-quotes a value for embedding in a compose YAML scalar.
// compose treats quoted strings literally, so this is the same escaping
// discipline as the shell quote helper.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
