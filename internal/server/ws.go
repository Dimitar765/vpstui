package server

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// handleWS upgrades GET /api/ws (auth via ?token=, resolved by the auth
// middleware) and streams hub frames until the client goes away. Inbound
// messages are only read to detect close; the protocol is server→client.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ch, cancel := s.hub.Subscribe()
	defer cancel()

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	go func() {
		// Reader: any inbound frame or error (close, ping timeout) ends the
		// connection. No client→server protocol yet.
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
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}
