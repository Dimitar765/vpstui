// Package hubclient is the TUI's client for the vpstui-server daemon: the
// REST calls behind the hub screens and the WebSocket watches that keep them
// live. Every method is context-driven; the watch functions unwind completely
// when their context is canceled, and their returned stop func does the same
// and then waits, so callers that want a synchronous shutdown have one.
package hubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"vpstui/internal/apitypes"
)

// PollInterval paces the fallback when the fleet WebSocket is unavailable.
// A var so tests can shrink it.
var PollInterval = 15 * time.Second

// Client talks to one vpstui-server daemon. It is safe for concurrent use;
// the TUI keeps exactly one and rebuilds it when the hub URL or token changes.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// New builds a client for the daemon at baseURL. A missing scheme is filled
// with http://; any path the user typed past the host is kept.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 20 * time.Second},
	}
}

// BaseURL is the normalized base the client was built from.
func (c *Client) BaseURL() string { return c.baseURL }

// ---------------------------------------------------------------- REST

// get issues an authenticated GET and decodes the JSON body into v.
func (c *Client) get(ctx context.Context, path string, v any) error {
	return c.do(ctx, http.MethodGet, path, nil, v)
}

// post issues an authenticated POST with an optional JSON body.
func (c *Client) post(ctx context.Context, path string, body any, v any) error {
	return c.do(ctx, http.MethodPost, path, body, v)
}

func (c *Client) do(ctx context.Context, method, path string, body, v any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := readErrBody(res)
		return fmt.Errorf("%s %s: %s", method, path, msg)
	}
	if v == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// readErrBody surfaces the daemon's {"error": …} message, falling back to the
// bare status so a non-JSON error page still makes a readable failure.
func readErrBody(res *http.Response) string {
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if err != nil {
		return res.Status
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return fmt.Sprintf("%d %s", res.StatusCode, e.Error)
	}
	return res.Status
}

// Fleet returns every registered server with its latest monitoring snapshot.
func (c *Client) Fleet(ctx context.Context) ([]apitypes.FleetServer, error) {
	var out apitypes.FleetResponse
	if err := c.get(ctx, "/api/fleet", &out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// Apps returns every deployable application.
func (c *Client) Apps(ctx context.Context) ([]apitypes.App, error) {
	var out []apitypes.App
	if err := c.get(ctx, "/api/apps", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Projects returns the project rows; pair with Environments for the labels.
func (c *Client) Projects(ctx context.Context) ([]apitypes.Project, error) {
	var out []apitypes.Project
	if err := c.get(ctx, "/api/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectDetail is GET /api/projects/{id}: the project row plus its
// environments inline (the daemon has no separate environments-list route).
type ProjectDetail struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Environments []apitypes.Environment `json:"environments"`
}

// Project returns one project with its environments.
func (c *Client) Project(ctx context.Context, projectID string) (ProjectDetail, error) {
	var out ProjectDetail
	err := c.get(ctx, "/api/projects/"+projectID, &out)
	return out, err
}

// Deploy queues a deployment of the app and returns the queued deployment id.
func (c *Client) Deploy(ctx context.Context, appID string) (string, error) {
	return c.deployAction(ctx, "/api/apps/"+appID+"/deploy")
}

// Rollback redeploys the app's last known-good release and returns the queued
// deployment id.
func (c *Client) Rollback(ctx context.Context, appID string) (string, error) {
	return c.deployAction(ctx, "/api/apps/"+appID+"/rollback")
}

func (c *Client) deployAction(ctx context.Context, path string) (string, error) {
	var out struct {
		DeploymentID string `json:"deploymentId"`
	}
	if err := c.post(ctx, path, nil, &out); err != nil {
		return "", err
	}
	if out.DeploymentID == "" {
		return "", fmt.Errorf("%s: empty deploymentId", path)
	}
	return out.DeploymentID, nil
}

// Deployments returns the deployment history of one app, newest first.
func (c *Client) Deployments(ctx context.Context, appID string) ([]apitypes.Deployment, error) {
	var out []apitypes.Deployment
	if err := c.get(ctx, "/api/apps/"+appID+"/deployments", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Deployment returns a single deployment run.
func (c *Client) Deployment(ctx context.Context, id string) (apitypes.Deployment, error) {
	var out apitypes.Deployment
	err := c.get(ctx, "/api/deployments/"+id, &out)
	return out, err
}

// ---------------------------------------------------------------- watches

func (c *Client) wsURL(path string) string {
	u := c.baseURL + path
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		return "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u
}

// dial upgrades a WS endpoint with the bearer token in the Authorization
// header (the daemon also accepts ?token= for browsers, which cannot set
// headers on WebSocket).
func (c *Client) dial(ctx context.Context, path string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, c.wsURL(path), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}},
		HTTPClient: c.hc,
	})
	if err != nil {
		return nil, err
	}
	// The daemon never sends inbound-protocol requirements; stop it from
	// buffering pings behind a reader that never calls the read-side.
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

// sendFrame delivers one frame unless the watch is ending. Returning false
// means the receiver is gone or the context canceled: unwind immediately.
func sendFrame[T any](ctx context.Context, ch chan<- T, v T) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- v:
		return true
	}
}

// WatchFleet streams fleet snapshots from GET /api/ws until the context is
// canceled or stop is called. Frames are pushed after every monitoring
// snapshot that changes anything, so no polling is needed while the WS is up.
// If the WS cannot be dialed or drops, the watch falls back to polling
// GET /api/fleet every PollInterval for the rest of the watch — fleet state is
// a whole-table replace, so a missed push costs nothing.
func (c *Client) WatchFleet(ctx context.Context) (<-chan apitypes.FleetUpdateFrame, func(), error) {
	wctx, cancel := context.WithCancel(ctx)
	ch := make(chan apitypes.FleetUpdateFrame, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(ch)
		c.watchFleet(wctx, ch)
	}()
	return ch, func() { cancel(); <-done }, nil
}

func (c *Client) watchFleet(ctx context.Context, ch chan<- apitypes.FleetUpdateFrame) {
	conn, err := c.dial(ctx, "/api/ws")
	if err == nil {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				break // dropped mid-stream: fall through to the poll fallback
			}
			var f apitypes.FleetUpdateFrame
			if json.Unmarshal(data, &f) != nil || f.Type != apitypes.FrameFleetUpdate {
				continue // alerts and deploy frames belong to other watchers
			}
			if !sendFrame(ctx, ch, f) {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}
	c.pollFleet(ctx, ch)
}

func (c *Client) pollFleet(ctx context.Context, ch chan<- apitypes.FleetUpdateFrame) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		servers, err := c.Fleet(ctx)
		if err != nil && ctx.Err() != nil {
			return
		}
		if err == nil {
			if !sendFrame(ctx, ch, apitypes.FleetUpdateFrame{
				Type: apitypes.FrameFleetUpdate, Servers: servers,
			}) {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// WatchAlerts streams alert-rule transitions from GET /api/ws. Alerts are
// push-only — the daemon exposes no REST poll for them — so when the WS is
// unavailable the channel simply closes when the watch ends; the fleet poll
// still carries the rest of the screen. Rule transitions are rare, so a
// dropped stream costs at most a stale badge until the next reconnect.
func (c *Client) WatchAlerts(ctx context.Context) (<-chan apitypes.AlertFrame, func(), error) {
	wctx, cancel := context.WithCancel(ctx)
	ch := make(chan apitypes.AlertFrame, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(ch)
		conn, err := c.dial(wctx, "/api/ws")
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			_, data, err := conn.Read(wctx)
			if err != nil {
				return
			}
			var f apitypes.AlertFrame
			if json.Unmarshal(data, &f) != nil || f.Type != apitypes.FrameAlert {
				continue
			}
			if !sendFrame(wctx, ch, f) {
				return
			}
		}
	}()
	return ch, func() { cancel(); <-done }, nil
}

// WatchDeployment streams one deployment's log over
// GET /api/deployments/{id}/logs: the stored file replays first, then the
// connection tails live deploy.log frames until the terminal deploy.status
// frame, which is delivered as a final DeployLogFrame with Done set. The
// channel closes after that frame.
//
// Fallback: while the WS cannot be dialed (or drops before any line was
// streamed) the watch retries the dial on the poll tick and checks
// GET /api/deployments/{id} for a terminal status, so the view still learns
// the outcome — without lines — when only the WS is broken. Once lines have
// been streamed, a dropped connection polls for the terminal status only:
// re-dialing would replay the file and duplicate what the view already holds.
func (c *Client) WatchDeployment(ctx context.Context, id string) (<-chan apitypes.DeployLogFrame, func(), error) {
	wctx, cancel := context.WithCancel(ctx)
	ch := make(chan apitypes.DeployLogFrame, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(ch)
		c.watchDeployment(wctx, id, ch)
	}()
	return ch, func() { cancel(); <-done }, nil
}

func (c *Client) watchDeployment(ctx context.Context, id string, ch chan<- apitypes.DeployLogFrame) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	streamed := false // at least one line went out on this watch
	for {
		sent, terminal := c.streamDeployment(ctx, id, ch)
		if terminal || ctx.Err() != nil {
			return
		}
		// A connected stream that dropped before the terminal frame has
		// already delivered its replay: fall back to status polls, because a
		// re-dial would replay the stored file and duplicate every line the
		// view holds. A watch that never got a connection keeps retrying the
		// dial on each tick, so a busy daemon is picked up the moment it can
		// serve the stream.
		streamed = streamed || sent
		if c.awaitDeploymentEnd(ctx, id, ch, t, streamed) {
			return
		}
	}
}

// streamDeployment runs one WS attempt. It reports whether any line was
// delivered and whether the terminal frame went out.
func (c *Client) streamDeployment(ctx context.Context, id string, ch chan<- apitypes.DeployLogFrame) (sent, terminal bool) {
	conn, err := c.dial(ctx, "/api/deployments/"+id+"/logs")
	if err != nil {
		return false, false
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	doneSent := false
	sendDone := func() {
		if doneSent {
			return
		}
		doneSent = true
		if sendFrame(ctx, ch, apitypes.DeployLogFrame{
			Type: apitypes.FrameDeployLog, DeploymentID: id, Done: true,
		}) {
			terminal = true
		}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return sent, terminal
		}
		var f apitypes.DeployLogFrame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.Type {
		case apitypes.FrameDeployLog:
			if f.Done {
				sendDone()
				continue
			}
			sent = true
			if !sendFrame(ctx, ch, f) {
				return sent, terminal
			}
		case apitypes.FrameDeployStatus:
			sendDone()
			return sent, terminal
		}
	}
}

// awaitDeploymentEnd waits one poll tick, checking the deployment status for a
// terminal value each round (a watch that streamed its lines is over for log
// purposes and only waits for the status to stop ticking promptly). It reports
// whether the watch is finished.
func (c *Client) awaitDeploymentEnd(ctx context.Context, id string, ch chan<- apitypes.DeployLogFrame, t *time.Ticker, streamed bool) bool {
	dep, err := c.Deployment(ctx, id)
	if err == nil {
		switch dep.Status {
		case apitypes.DepSuccess, apitypes.DepFailed, apitypes.DepCanceled:
			if !streamed {
				// No line ever reached the view: say the stream is over so a
				// degraded watch still terminates visibly.
				sendFrame(ctx, ch, apitypes.DeployLogFrame{
					Type: apitypes.FrameDeployLog, DeploymentID: id, Done: true,
				})
			}
			return true
		}
	} else if ctx.Err() != nil {
		return true
	}
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
