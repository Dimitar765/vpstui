package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"vpstui/internal/models"
	"vpstui/internal/sshclient"
)

type Executor struct {
	conn *sshclient.Conn
}

func New(conn *sshclient.Conn) *Executor {
	return &Executor{conn: conn}
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (e *Executor) exec(ctx context.Context, timeout time.Duration, cmd string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, errOut, err := e.conn.Exec(cctx, cmd)
	if err != nil {
		return out, friendly(errOut, err)
	}
	return out, nil
}

func friendly(stderr string, err error) error {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "command not found"):
		return fmt.Errorf("docker is not installed or not in PATH on this host")
	case strings.Contains(low, "cannot connect to the docker daemon"):
		return fmt.Errorf("docker daemon is not running on this host")
	case strings.Contains(low, "permission denied") && strings.Contains(low, "docker"):
		return fmt.Errorf("permission denied accessing /var/run/docker.sock — add the user to the docker group")
	case strings.Contains(err.Error(), "context deadline exceeded"):
		return fmt.Errorf("command timed out: %w", err)
	default:
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
}

type psJSON struct {
	ID        string `json:"Id"`
	Names     string `json:"Names"`
	Image     string `json:"Image"`
	State     string `json:"State"`
	Status    string `json:"Status"`
	CreatedAt string `json:"CreatedAt"`
}

func (e *Executor) ListContainers(ctx context.Context) ([]models.Container, error) {
	out, err := e.exec(ctx, 15*time.Second, `docker ps -a --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	var res []models.Container
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var j psJSON
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			continue
		}
		res = append(res, models.Container{
			ID: j.ID, Names: j.Names, Image: j.Image,
			State: j.State, Status: j.Status, CreatedAt: j.CreatedAt,
		})
	}
	return res, nil
}

type statsJSON struct {
	ID       string `json:"Id"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
}

func (e *Executor) ContainerStats(ctx context.Context) ([]models.ContainerStats, error) {
	out, err := e.exec(ctx, 15*time.Second, `docker stats --no-stream --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	var res []models.ContainerStats
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var j statsJSON
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			continue
		}
		res = append(res, models.ContainerStats{
			ID: j.ID, Name: j.Name, CPUPerc: j.CPUPerc,
			MemUsage: j.MemUsage, MemPerc: j.MemPerc,
		})
	}
	return res, nil
}

func (e *Executor) HostStats(ctx context.Context) (models.HostStats, error) {
	cmd := `sh -c 'cat /proc/loadavg; cat /proc/uptime; grep -E "^Mem(Total|Available):" /proc/meminfo; nproc'`
	out, err := e.exec(ctx, 10*time.Second, cmd)
	if err != nil {
		return models.HostStats{}, err
	}
	return parseHostStats(out), nil
}

// parseHostStats parses the stdout produced by the HostStats probe command.
// Extracted from HostStats without behavior change to make it testable.
func parseHostStats(out string) models.HostStats {
	var hs models.HostStats
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			hs.MemTotalMB = kbMB(fields[1])
		case "MemAvailable:":
			hs.MemUsedMB = hs.MemTotalMB - kbMB(fields[1])
		default:
			switch len(fields) {
			case 5: // loadavg: 1 5 15 procs last
				hs.Load1 = atof(fields[0])
				hs.Load5 = atof(fields[1])
				hs.Load15 = atof(fields[2])
			case 2: // uptime: secs idle
				hs.UptimeSec = atof(fields[0])
			case 1: // nproc
				hs.NumCPU, _ = strconv.Atoi(fields[0])
			}
		}
	}
	return hs
}

func kbMB(s string) int {
	n, _ := strconv.Atoi(s)
	return n / 1024
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func (e *Executor) Start(ctx context.Context, id string) error {
	_, err := e.exec(ctx, 30*time.Second, "docker start "+quote(id))
	return err
}

func (e *Executor) Stop(ctx context.Context, id string) error {
	_, err := e.exec(ctx, 30*time.Second, "docker stop "+quote(id))
	return err
}

func (e *Executor) Restart(ctx context.Context, id string) error {
	_, err := e.exec(ctx, 30*time.Second, "docker restart "+quote(id))
	return err
}

func (e *Executor) StreamLogs(ctx context.Context, id string) (io.ReadCloser, error) {
	return e.conn.StreamExec(ctx, "docker logs -f --tail 200 "+quote(id))
}
