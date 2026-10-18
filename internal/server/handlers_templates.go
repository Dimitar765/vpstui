package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/store"
	"vpstui/internal/templates"
)

// registerTemplates: the one-click catalog and its deploy endpoint.
func (s *Server) registerTemplates(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/templates", s.handleTemplatesList)
	mux.HandleFunc("POST /api/templates/{id}/deploy", s.handleTemplateDeploy)
}

func toTemplateResp(t templates.Template) apitypes.Template {
	return apitypes.Template{
		ID: t.ID, Name: t.Name, Description: t.Description,
		Category: t.Category, Logo: t.Logo, Type: t.Type, Port: t.Port,
	}
}

func (s *Server) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	list := templates.List()
	out := make([]apitypes.Template, 0, len(list))
	for _, t := range list {
		out = append(out, toTemplateResp(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// templateDeployReq: name defaults to the catalog name; domains attach the
// app to the managed proxy. environmentId is required from the GUI, EXCEPT
// that a null environmentId bootstraps a `Default` project + `production`
// environment when the database is empty (first-run convenience for curl /
// TUI hub; documented contract).
type templateDeployReq struct {
	ServerID      string   `json:"serverId"`
	EnvironmentID *string  `json:"environmentId"`
	Name          string   `json:"name"`
	Domains       []string `json:"domains"`
}

func (s *Server) handleTemplateDeploy(w http.ResponseWriter, r *http.Request) {
	var req templateDeployReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	tpl, err := templates.Get(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.db.ServerByID(req.ServerID); err != nil {
		writeErr(w, http.StatusBadRequest, "server not found")
		return
	}
	envID := ""
	if req.EnvironmentID != nil && *req.EnvironmentID != "" {
		if _, err := s.db.EnvironmentByID(*req.EnvironmentID); err != nil {
			writeErr(w, http.StatusBadRequest, "environment not found")
			return
		}
		envID = *req.EnvironmentID
	} else {
		envID, err = s.defaultEnvironmentID()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	app := tpl.App(req.ServerID, envID, req.Name, req.Domains)
	app.ID = models.NewID()
	app.WebhookToken = newWebhookToken()
	if err := s.db.CreateApp(app); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	depID, err := s.engine.Enqueue(app.ID, apitypes.TriggerManual)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"appId":        app.ID,
		"deploymentId": depID,
	})
}

// defaultEnvironmentID bootstraps `Default`/`production` on an empty
// database; with existing rows it refuses (the client must pick one).
func (s *Server) defaultEnvironmentID() (string, error) {
	projects, err := s.db.Projects()
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		p := store.Project{ID: models.NewID(), Name: "Default", CreatedAt: time.Now().UTC()}
		if err := s.db.CreateProject(p); err != nil {
			return "", err
		}
		e := store.Environment{ID: models.NewID(), ProjectID: p.ID, Name: "production"}
		if err := s.db.CreateEnvironment(e); err != nil {
			return "", err
		}
		return e.ID, nil
	}
	envs, err := s.db.EnvironmentsByProject(projects[0].ID)
	if err != nil {
		return "", err
	}
	if len(envs) == 0 {
		return "", errors.New("environmentId is required")
	}
	return envs[0].ID, nil
}
