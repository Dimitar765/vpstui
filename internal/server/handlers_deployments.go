package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"

	"vpstui/internal/apitypes"
	"vpstui/internal/store"
)

// registerDeployments: the read side of the deploy history.
func (s *Server) registerDeployments(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/deployments/{id}", s.handleDeploymentGet)
	mux.HandleFunc("GET /api/deployments/{id}/logs", s.handleDeploymentLogs)
}

func (s *Server) handleDeploymentGet(w http.ResponseWriter, r *http.Request) {
	dep, err := s.db.DeploymentByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toDeploymentResp(dep))
}

// handleDeploymentLogs serves the deploy log over WS: the stored file is
// replayed first, then — while the deployment is still queued/running — the
// connection tails live deploy.log/deploy.status hub frames for it. Terminal
// deployments get the replay plus a closing done frame.
func (s *Server) handleDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	dep, err := s.db.DeploymentByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	write := func(frame any) bool {
		b, err := json.Marshal(frame)
		if err != nil {
			return true
		}
		wctx, wcancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageText, b) == nil
	}

	if f, err := os.Open(dep.LogFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() && write(apitypes.DeployLogFrame{
			Type: apitypes.FrameDeployLog, DeploymentID: dep.ID, Line: sc.Text(),
		}) {
		}
		f.Close()
	}

	switch dep.Status {
	case apitypes.DepQueued, apitypes.DepRunning:
		s.tailDeployment(w, r, c, dep, write)
	default:
		write(apitypes.DeployLogFrame{Type: apitypes.FrameDeployLog, DeploymentID: dep.ID, Done: true})
		write(apitypes.DeployStatusFrame{Type: apitypes.FrameDeployStatus, DeploymentID: dep.ID, Status: dep.Status})
	}
}

// tailDeployment forwards hub frames for this deployment until the terminal
// deploy.status frame or the client goes away.
func (s *Server) tailDeployment(w http.ResponseWriter, r *http.Request, c *websocket.Conn, dep store.Deployment, write func(any) bool) {
	ch, cancel := s.hub.Subscribe()
	defer cancel()

	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	go func() {
		// Detect client close; the protocol stays server→client.
		defer stop()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			var frame struct {
				Type         string `json:"type"`
				DeploymentID string `json:"deploymentId"`
				Status       string `json:"status"`
				Line         string `json:"line"`
				Done         bool   `json:"done"`
			}
			if json.Unmarshal(b, &frame) != nil || frame.DeploymentID != dep.ID {
				continue
			}
			wctx, wcancel := context.WithTimeout(r.Context(), 10*time.Second)
			if err := c.Write(wctx, websocket.MessageText, b); err != nil {
				wcancel()
				return
			}
			wcancel()
			if frame.Type == apitypes.FrameDeployStatus {
				return // terminal
			}
		}
	}
}
