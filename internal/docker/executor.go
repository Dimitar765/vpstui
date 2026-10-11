package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
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
	Networks  string `json:"Networks"`
}

func (e *Executor) ListContainers(ctx context.Context) ([]models.Container, error) {
	out, err := e.exec(ctx, 15*time.Second, `docker ps -a --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	return parsePs(out), nil
}

// parsePs parses the NDJSON produced by `docker ps -a --format '{{json .}}'`.
// Extracted from ListContainers to make it testable. Its IDs are docker's
// 12-char short form, which is the app's canonical container id everywhere:
// the table, action args, and graph node ids.
func parsePs(out string) []models.Container {
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
		c := models.Container{
			ID: j.ID, Names: j.Names, Image: j.Image,
			State: j.State, Status: j.Status, CreatedAt: j.CreatedAt,
			Labels: j.Labels,
		}
		if j.Networks != "" {
			c.Networks = strings.Split(j.Networks, ",")
			sort.Strings(c.Networks)
		}
		res = append(res, c)
	}
	return res
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

// peakIDPattern guards what the peaks probe interpolates into a shell glob. The
// app's canonical container id is docker's 12-char short hex form, so anything
// else — including a name, which is user- and image-controlled — is dropped
// rather than quoted into a remote command.
var peakIDPattern = regexp.MustCompile(`^[0-9a-f]{6,64}$`)

// peaksScript reads, per container, the counters the kernel has been keeping
// since the container started: memory.peak (cgroup v2) or
// memory.max_usage_in_bytes (v1), and cumulative CPU plus the throttling
// counters. One docker inspect supplies the start time every id is keyed to.
//
// It prints one tab-separated row per id — id, peak bytes, CPU microseconds,
// nr_throttled, nr_periods, started at — and "-" for anything the host does not
// expose. It uses only POSIX syntax and no single quotes, so the whole script
// survives quote().
const peaksScript = `
info=$(docker inspect -f "{{.Id}} {{.State.StartedAt}}" "$@" 2>/dev/null)
one() { for d in $1; do if [ -d "$d" ]; then printf "%s" "$d"; return; fi; done; }
rd() { if [ -f "$1" ]; then cat "$1" 2>/dev/null; fi; }
kv() { printf "%s\n" "$1" | grep "^$2 " | cut -d" " -f2; }
for id in "$@"; do
  start=$(printf "%s\n" "$info" | grep "^$id" | head -n 1 | cut -d" " -f2)
  v2=$(one "/sys/fs/cgroup/system.slice/docker-$id*.scope")
  if [ -z "$v2" ]; then v2=$(one "/sys/fs/cgroup/docker/$id*"); fi
  v1m=$(one "/sys/fs/cgroup/memory/system.slice/docker-$id*")
  if [ -z "$v1m" ]; then v1m=$(one "/sys/fs/cgroup/memory/docker/$id*"); fi
  v1c=$(one "/sys/fs/cgroup/cpu,cpuacct/system.slice/docker-$id*")
  if [ -z "$v1c" ]; then v1c=$(one "/sys/fs/cgroup/cpuacct/system.slice/docker-$id*"); fi
  if [ -z "$v1c" ]; then v1c=$(one "/sys/fs/cgroup/cpu/system.slice/docker-$id*"); fi
  if [ -z "$v1c" ]; then v1c=$(one "/sys/fs/cgroup/cpuacct/docker/$id*"); fi
  if [ -z "$v1c" ]; then v1c=$(one "/sys/fs/cgroup/cpu,cpuacct/docker/$id*"); fi
  peak=$(rd "$v2/memory.peak")
  if [ -z "$peak" ]; then peak=$(rd "$v1m/memory.max_usage_in_bytes"); fi
  stat=$(rd "$v2/cpu.stat")
  usec=$(kv "$stat" usage_usec)
  thr=$(kv "$stat" nr_throttled)
  per=$(kv "$stat" nr_periods)
  if [ -z "$usec" ]; then
    usec=$(rd "$v1c/cpuacct.usage")
    if [ -n "$usec" ]; then usec=$(expr "$usec" / 1000); fi
    cstat=$(rd "$v1c/cpu.stat")
    thr=$(kv "$cstat" nr_throttled)
    per=$(kv "$cstat" nr_periods)
  fi
  if [ -z "$peak" ]; then peak=-; fi
  if [ -z "$usec" ]; then usec=-; fi
  if [ -z "$thr" ]; then thr=-; fi
  if [ -z "$per" ]; then per=-; fi
  if [ -z "$start" ]; then start=-; fi
  printf "%s\t%s\t%s\t%s\t%s\t%s\n" "$id" "$peak" "$usec" "$thr" "$per" "$start"
done
`

// ContainerPeaks reads the kernel's all-time per-container maxima in one
// read-only round trip. Ids that are not the short hex form docker reports are
// skipped, and a host with no readable cgroup returns rows whose counters are
// models.Unknown rather than an error — the screen can then show "-" beside a
// container it is still measuring.
func (e *Executor) ContainerPeaks(ctx context.Context, ids []string) ([]models.ContainerPeak, error) {
	safe := validPeakIDs(ids)
	if len(safe) == 0 {
		return nil, nil
	}
	out, err := e.exec(ctx, 10*time.Second, PeaksCommand(safe))
	if err != nil {
		return nil, err
	}
	return parseContainerPeaks(out), nil
}

// validPeakIDs keeps only the ids the probe may interpolate into a glob.
func validPeakIDs(ids []string) []string {
	var safe []string
	for _, id := range ids {
		if peakIDPattern.MatchString(id) {
			safe = append(safe, id)
		}
	}
	return safe
}

// PeaksCommand builds the probe for these ids, which must already be filtered by
// validPeakIDs. Each id is a separate quoted argument, so the script's "$@" is
// the only place they appear and one hostile string cannot reach the shell.
func PeaksCommand(ids []string) string {
	cmd := "sh -c " + quote(peaksScript) + " sh"
	for _, id := range ids {
		cmd += " " + quote(id)
	}
	return cmd
}

// parseContainerPeaks parses the rows the probe prints. Extracted to make it
// testable: a field that is "-", unparseable or missing makes that counter
// models.Unknown, never a silent zero.
func parseContainerPeaks(out string) []models.ContainerPeak {
	var res []models.ContainerPeak
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 5 || !peakIDPattern.MatchString(f[0]) {
			continue
		}
		p := models.ContainerPeak{
			ID:           f[0],
			MemPeakBytes: numOr(f[1]),
			CPUUsec:      numOr(f[2]),
			Throttled:    numOr(f[3]),
			Periods:      numOr(f[4]),
		}
		if len(f) > 5 && f[5] != "-" {
			p.StartedAt = f[5]
		}
		res = append(res, p)
	}
	return res
}

func numOr(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return models.Unknown
	}
	return n
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

func (e *Executor) StreamLogs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	return e.conn.StreamExec(ctx, "docker logs -f --timestamps --tail "+strconv.Itoa(tail)+" "+quote(id))
}

// ErrSweepCommand builds one POSIX-shell loop that dumps every container's
// recent logs — running or not — prefixed with "id<TAB>name<TAB>" so a single
// SSH round trip carries the whole host. since is a validated duration
// re-rendered in seconds and tail a clamped int, so nothing user-typed reaches
// the shell. `|| true` plus the final `exit 0` matter: a container removed
// between ps and logs would otherwise make docker logs fail, the loop would
// return that status, and friendly() would turn a nearly-complete payload into
// an error screen.
func ErrSweepCommand(since string, tail int) string {
	return "docker ps -a --format '{{.ID}} {{.Names}}' | while read -r id name; do " +
		"docker logs --timestamps --since " + quote(since) + " --tail " + strconv.Itoa(tail) + " \"$id\" 2>&1 | " +
		"awk -v p=\"$id\" -v n=\"$name\" 'BEGIN{OFS=\"\\t\"}{print p,n,$0}' || true; done; exit 0"
}

// SweepErrors returns the raw sweep payload for this host. On error it still
// returns whatever was received, so one bad container cannot hide the rest.
func (e *Executor) SweepErrors(ctx context.Context, since string, tail int) (string, error) {
	return e.exec(ctx, 25*time.Second, ErrSweepCommand(since, tail))
}

// ShellCommand builds the SSH-level command for an interactive container
// shell, preferring bash and falling back to /bin/sh. A failed `exec` exits
// the wrapper shell, so bash is probed with command -v first. term must
// already be sanitized by the caller.
func ShellCommand(id, term string) string {
	return "docker exec -i -t -e TERM=" + quote(term) + " " + quote(id) +
		" sh -c " + quote("command -v bash >/dev/null 2>&1 && exec bash || exec /bin/sh")
}

// ProbeShell checks that the container has a POSIX shell, without a PTY.
func (e *Executor) ProbeShell(ctx context.Context, id string) error {
	_, err := e.exec(ctx, 10*time.Second, "docker exec -i "+quote(id)+" sh -c exit")
	return err
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
