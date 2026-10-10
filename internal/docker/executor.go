package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
	Labels    string `json:"Labels"`
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
			Labels: j.Labels,
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

type dfJSON struct {
	Type        string `json:"Type"`
	Total       string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

func (e *Executor) SystemDiskUsage(ctx context.Context) ([]models.DiskObject, error) {
	out, err := e.exec(ctx, 20*time.Second, `docker system df --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	return parseDF(out), nil
}

// parseDF parses the NDJSON produced by `docker system df --format '{{json .}}'`.
func parseDF(out string) []models.DiskObject {
	var res []models.DiskObject
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var j dfJSON
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			continue
		}
		res = append(res, models.DiskObject{
			Type: j.Type, Total: j.Total, Active: j.Active,
			Size: j.Size, Reclaimable: j.Reclaimable,
		})
	}
	return res
}

func (e *Executor) PruneImages(ctx context.Context) error {
	_, err := e.exec(ctx, 120*time.Second, `docker image prune -f`)
	return err
}

func (e *Executor) PruneSystem(ctx context.Context) error {
	_, err := e.exec(ctx, 120*time.Second, `docker system prune -f`)
	return err
}

func (e *Executor) Rm(ctx context.Context, id string) error {
	_, err := e.exec(ctx, 30*time.Second, "docker rm "+quote(id))
	return err
}

func (e *Executor) StreamLogs(ctx context.Context, id string) (io.ReadCloser, error) {
	return e.conn.StreamExec(ctx, "docker logs -f --tail 200 "+quote(id))
}

func (e *Executor) Inspect(ctx context.Context, id string) (models.ContainerDetail, error) {
	out, err := e.exec(ctx, 15*time.Second, "docker inspect "+quote(id))
	if err != nil {
		return models.ContainerDetail{}, err
	}
	return parseInspect(out)
}

type inspectJSON struct {
	Name    string
	Created string
	Config  struct {
		Image string
	}
	State struct {
		Status       string
		RestartCount int
	}
	Mounts []struct {
		Type        string
		Source      string
		Destination string
		RW          bool
	}
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		}
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}
	}
}

func parseInspect(out string) (models.ContainerDetail, error) {
	var arr []inspectJSON
	if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) == 0 {
		return models.ContainerDetail{}, fmt.Errorf("could not parse docker inspect output")
	}
	j := arr[0]
	d := models.ContainerDetail{
		Name:         strings.TrimPrefix(j.Name, "/"),
		Image:        j.Config.Image,
		Status:       j.State.Status,
		Created:      j.Created,
		RestartCount: j.State.RestartCount,
	}
	for name, nw := range j.NetworkSettings.Networks {
		if nw.IPAddress == "" {
			continue
		}
		d.Networks = append(d.Networks, models.NetworkAddr{Name: name, IPAddress: nw.IPAddress})
	}
	sort.Slice(d.Networks, func(i, k int) bool { return d.Networks[i].Name < d.Networks[k].Name })
	var portKeys []string
	for p := range j.NetworkSettings.Ports {
		portKeys = append(portKeys, p)
	}
	sort.Strings(portKeys)
	for _, p := range portKeys {
		binds := j.NetworkSettings.Ports[p]
		if len(binds) == 0 {
			d.Ports = append(d.Ports, p+" -> (not published)")
			continue
		}
		for _, b := range binds {
			d.Ports = append(d.Ports, p+" -> "+b.HostIP+":"+b.HostPort)
		}
	}
	for _, m := range j.Mounts {
		rw := "ro"
		if m.RW {
			rw = "rw"
		}
		d.Mounts = append(d.Mounts, fmt.Sprintf("%s: %s -> %s (%s)", strings.ToLower(m.Type), m.Source, m.Destination, rw))
	}
	return d, nil
}
