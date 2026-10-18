// Package templates is the one-click service catalog: a set of embedded YAML
// entries resolved into store.App rows by the template deploy endpoint. Image
// templates render through the normal engine path (source_config.image/port/
// volumes/env); compose templates carry a full multi-service compose document
// with {{id}} placeholders for persistent named volumes.
package templates

import (
	"embed"
	"fmt"
	"time"

	"vpstui/internal/store"

	"gopkg.in/yaml.v3"
)

//go:embed catalog/*.yaml
var catalog embed.FS

// Template is one catalog entry. Type is "image" (single service rendered by
// RenderCompose) or "compose" (ComposeYAML deployed verbatim after {{id}}
// substitution, with vpstui's network and Traefik labels merged onto
// PublicService). Volumes use "{{id}}-name:/container/path" form; {{id}}
// becomes the APP id so named volumes survive redeploys and rollbacks.
type Template struct {
	ID            string            `yaml:"id"`
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	Category      string            `yaml:"category"`
	Logo          string            `yaml:"logo"`
	Type          string            `yaml:"type"`
	Image         string            `yaml:"image"`
	Port          int               `yaml:"port"`
	Volumes       []string          `yaml:"volumes"`
	Env           map[string]string `yaml:"env"`
	PublicService string            `yaml:"public_service"`
	ComposeYAML   string            `yaml:"compose"`
}

// List returns the whole catalog in file-name order (stable for the GUI grid).
func List() []Template {
	entries, err := catalog.ReadDir("catalog")
	if err != nil {
		return nil
	}
	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		t, err := parse("catalog/" + e.Name())
		if err != nil {
			continue // a broken entry must not blank the grid; vetted by tests
		}
		out = append(out, t)
	}
	return out
}

// Get returns one catalog entry; unknown ids produce an error the handler
// surfaces as 404.
func Get(id string) (Template, error) {
	for _, t := range List() {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("template %q: %w", id, store.ErrNotFound)
}

func parse(name string) (Template, error) {
	b, err := catalog.ReadFile(name)
	if err != nil {
		return Template{}, err
	}
	var t Template
	if err := yaml.Unmarshal(b, &t); err != nil {
		return Template{}, fmt.Errorf("%s: %w", name, err)
	}
	if t.ID == "" || t.Type == "" {
		return Template{}, fmt.Errorf("%s: id and type are required", name)
	}
	switch t.Type {
	case "image":
		if t.Image == "" {
			return Template{}, fmt.Errorf("%s: image required for type image", name)
		}
	case "compose":
		if t.ComposeYAML == "" || t.PublicService == "" {
			return Template{}, fmt.Errorf("%s: compose and public_service required for type compose", name)
		}
	default:
		return Template{}, fmt.Errorf("%s: unknown type %q", name, t.Type)
	}
	return t, nil
}

// App resolves the catalog entry into an app row (source_type=template).
// The caller assigns ID/WebhookToken. SourceConfig carries everything the
// engine's renderer consumes: image/port/volumes/env for image templates,
// compose_yaml/public_service for compose templates.
func (t Template) App(serverID, environmentID, name string, domains []string) store.App {
	if name == "" {
		name = t.Name
	}
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	cfg := map[string]any{"template_id": t.ID}
	if t.Type == "compose" {
		cfg["compose_yaml"] = t.ComposeYAML
		cfg["public_service"] = t.PublicService
	} else {
		cfg["image"] = t.Image
		cfg["port"] = t.Port
		if len(t.Volumes) > 0 {
			vols := make([]any, len(t.Volumes))
			for i, v := range t.Volumes {
				vols[i] = v
			}
			cfg["volumes"] = vols
		}
		if len(t.Env) > 0 {
			e := map[string]any{}
			for k, v := range t.Env {
				e[k] = v
			}
			cfg["env"] = e
		}
	}
	if domains == nil {
		domains = []string{}
	}
	return store.App{
		EnvironmentID: environmentID,
		ServerID:      serverID,
		Name:          name,
		SourceType:    "template",
		SourceConfig:  cfg,
		EnvVars:       env,
		Domains:       domains,
		Port:          t.Port,
		AutoDeploy:    true,
		Status:        "undeployed",
		UpdatedAt:     time.Now().UTC(),
	}
}
