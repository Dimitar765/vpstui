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
	vp       viewport.Model
	buf      strings.Builder
	err      string
	running  bool

	cancel context.CancelFunc
	reader io.ReadCloser
}

func NewContainerView(id, name string, w, h int) *ContainerView {
	c := &ContainerView{id: id, name: name, running: true}
	c.vp = viewport.New(max(w-2, 20), max(h-4, 5))
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

func streamLogs(r io.ReadCloser, send func(tea.Msg)) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if send == nil {
			return
		}
		send(logChunkMsg{chunk: sc.Text() + "\n"})
	}
	err := sc.Err()
	if send != nil {
		if err != nil && !strings.Contains(err.Error(), "file already closed") {
			send(logEndMsg{err: err})
		} else {
			send(logEndMsg{})
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
	c.vp.Width = max(w-2, 20)
	c.vp.Height = max(h-4, 5)
}

func (c *ContainerView) Update(msg tea.Msg) (*ContainerView, tea.Cmd) {
	var cmd tea.Cmd
	c.vp, cmd = c.vp.Update(msg)
	return c, cmd
}

func (c *ContainerView) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("logs — "+c.name) + " " + styleMuted.Render("["+c.id[:min(len(c.id), 12)]+"]") + "\n")
	if c.err != "" {
		b.WriteString(styleErr.Render("stream ended: "+c.err) + "\n")
	} else if !c.running {
		b.WriteString(styleMuted.Render("stream ended") + "\n")
	} else {
		b.WriteString(styleMuted.Render("streaming…") + "\n")
	}
	b.WriteString(c.vp.View() + "\n")
	b.WriteString(helpLine("↑/↓", "scroll", "pgup/pgdn", "page", "esc", "back") + "\n")
	return b.String()
}
