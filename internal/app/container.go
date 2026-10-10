package app

import (
	"bufio"
	"context"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/docker"
)

type ContainerView struct {
	id, name string
	hostID   string
	gen      uint64
	vp       viewport.Model
	buf      strings.Builder
	err      string
	running  bool
	width    int
	height   int

	cancel context.CancelFunc
	reader io.ReadCloser
}

func NewContainerView(id, name string, w, h int) *ContainerView {
	c := &ContainerView{id: id, name: name, running: true, width: w, height: h}
	c.vp = viewport.New(max(w-6, 20), max(h-6, 5))
	return c
}

// Open starts the remote `docker logs -f` stream; the caller pumps lines to
// send via streamLogs.
func (c *ContainerView) Open(dock *docker.Executor, id string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := dock.StreamLogs(ctx, id)
	if err != nil {
		cancel()
		return nil, err
	}
	c.cancel = cancel
	c.reader = r
	return r, nil
}

func (c *ContainerView) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.reader != nil {
		c.reader.Close()
	}
	c.running = false
}

func (c *ContainerView) tag(hostID string, gen uint64) bool {
	return c.hostID == hostID && c.gen == gen
}

func streamLogs(r io.ReadCloser, send func(tea.Msg), t msgTag) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if send == nil {
			return
		}
		send(logChunkMsg{msgTag: t, chunk: sc.Text() + "\n"})
	}
	err := sc.Err()
	if send != nil {
		if err != nil && !strings.Contains(err.Error(), "file already closed") {
			send(logEndMsg{msgTag: t, err: err})
		} else {
			send(logEndMsg{msgTag: t})
		}
	}
}

func (c *ContainerView) Append(chunk string) {
	c.buf.WriteString(chunk)
	if c.buf.Len() > 512*1024 {
		trimmed := c.buf.String()[c.buf.Len()-256*1024:]
		c.buf.Reset()
		c.buf.WriteString("… (older lines dropped) …\n")
		c.buf.WriteString(trimmed)
	}
	atBottom := c.vp.AtBottom()
	c.vp.SetContent(c.buf.String())
	if atBottom || c.running {
		c.vp.GotoBottom()
	}
}

func (c *ContainerView) StreamDone(err error) {
	c.running = false
	if err != nil {
		c.err = err.Error()
	}
}

func (c *ContainerView) SetSize(w, h int) {
	c.width, c.height = w, h
	c.vp.Width = max(w-6, 20)
	c.vp.Height = max(h-6, 5)
}

func (c *ContainerView) Update(msg tea.Msg) (*ContainerView, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "g":
			c.vp.GotoTop()
			return c, nil
		case "G":
			c.vp.GotoBottom()
			return c, nil
		}
	}
	var cmd tea.Cmd
	c.vp, cmd = c.vp.Update(msg)
	return c, cmd
}

func (c *ContainerView) View() string {
	title := styleTitle.Render("logs — "+c.name) + " " + styleMuted.Render("["+c.id[:min(len(c.id), 12)]+"]")
	var status string
	switch {
	case c.err != "":
		status = styleErr.Render("stream ended: " + c.err)
	case !c.running:
		status = styleMuted.Render("stream ended")
	default:
		status = styleOK.Render("● streaming…")
	}
	body := panel(title, status+"\n"+c.vp.View(), c.width-2)
	return pinFooter(body, helpLine("↑/↓ j/k", "scroll", "pgup/pgdn", "page", "g/G", "top/bottom", "esc", "back"), c.width, c.height)
}
