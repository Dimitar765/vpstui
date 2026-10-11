package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"vpstui/internal/models"
	"vpstui/internal/sshclient"
)

func TestQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary", "mycontainer", "'mycontainer'"},
		{"embedded quote", "it's", `'it'\''s'`},
		{"empty string", "", "''"},
		{"only a quote", "'", `''\'''`},
		{"multiple quotes", "a'b'c", `'a'\''b'\''c'`},
		{"spaces preserved", "a b", "'a b'"},
		{"double quote untouched", `say "hi"`, `'say "hi"'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quote(tt.in); got != tt.want {
				t.Errorf("quote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestKbMB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"exact mebibyte", "1024", 1},
		{"two megabytes", "2048", 2},
		{"zero", "0", 0},
		{"rounds down", "1500", 1},
		{"below a KB", "512", 0},
		{"invalid parses as zero", "abc", 0},
		{"empty", "", 0},
		{"negative truncates toward zero", "-1500", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kbMB(tt.in); got != tt.want {
				t.Errorf("kbMB(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestAtof(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{"float", "1.5", 1.5},
		{"integer text", "42", 42},
		{"zero", "0", 0},
		{"exponent", "1e3", 1000},
		{"invalid", "abc", 0},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := atof(tt.in); got != tt.want {
				t.Errorf("atof(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFriendly(t *testing.T) {
	baseErr := errors.New("Process exited, status 1")
	tests := []struct {
		name        string
		stderr      string
		err         error
		wantMsg     string // exact message if non-empty
		wantContain string // substring check instead
		wantIs      error  // errors.Is target
	}{
		{
			name:        "docker not installed",
			stderr:      "sh: docker: command not found",
			err:         baseErr,
			wantContain: "docker is not installed",
		},
		{
			name:        "command not found is case insensitive",
			stderr:      "bash: DOCKER: Command Not Found",
			err:         baseErr,
			wantContain: "docker is not installed",
		},
		{
			name:        "daemon not running",
			stderr:      "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.",
			err:         baseErr,
			wantContain: "docker daemon is not running",
		},
		{
			name:        "permission denied on docker socket",
			stderr:      "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock",
			err:         baseErr,
			wantContain: "group",
		},
		{
			name:        "context deadline exceeded",
			stderr:      "",
			err:         context.DeadlineExceeded,
			wantContain: "command timed out",
			wantIs:      context.DeadlineExceeded,
		},
		{
			name:    "plain stderr passthrough",
			stderr:  "Error: No such container: abc",
			err:     baseErr,
			wantMsg: "Error: No such container: abc",
		},
		{
			name:    "stderr is trimmed",
			stderr:  "  noisy failure \n",
			err:     baseErr,
			wantMsg: "noisy failure",
		},
		{
			name:    "empty stderr falls back to err",
			stderr:  "",
			err:     errors.New("dial tcp: connection refused"),
			wantMsg: "dial tcp: connection refused",
		},
		{
			name:    "whitespace-only stderr falls back to err",
			stderr:  " \n\t ",
			err:     errors.New("boom"),
			wantMsg: "boom",
		},
		{
			name:    "permission denied without docker mention is passthrough",
			stderr:  "permission denied on /mnt/data",
			err:     baseErr,
			wantMsg: "permission denied on /mnt/data",
		},
		{
			// The auto-retry keys on this: friendly must not re-wrap the
			// typed signal into an unrecognizable message.
			name:   "connection loss passes through typed",
			stderr: "",
			err:    fmt.Errorf("%w: %v", sshclient.ErrConnLost, io.EOF),
			wantIs: sshclient.ErrConnLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendly(tt.stderr, tt.err)
			if got == nil {
				t.Fatal("friendly returned nil error")
			}
			switch {
			case tt.wantMsg != "":
				if got.Error() != tt.wantMsg {
					t.Errorf("friendly() error = %q, want %q", got.Error(), tt.wantMsg)
				}
			case tt.wantContain != "":
				if !strings.Contains(got.Error(), tt.wantContain) {
					t.Errorf("friendly() error = %q, want it to contain %q", got.Error(), tt.wantContain)
				}
			}
			if tt.wantIs != nil && !errors.Is(got, tt.wantIs) {
				t.Errorf("friendly() error %q does not wrap %v", got, tt.wantIs)
			}
		})
	}
}

func TestParseHostStats(t *testing.T) {
	const typical = "0.52 0.58 0.59 1/350 12345\n" +
		"12345.67 12000.00\n" +
		"MemTotal:       1048576 kB\n" +
		"MemAvailable:    524288 kB\n" +
		"4\n"

	tests := []struct {
		name string
		in   string
		want models.HostStats
	}{
		{
			name: "typical /proc output",
			in:   typical,
			want: models.HostStats{
				Load1: 0.52, Load5: 0.58, Load15: 0.59,
				MemTotalMB: 1024, MemUsedMB: 512,
				NumCPU: 4, UptimeSec: 12345.67,
			},
		},
		{
			name: "trailing newline tolerated",
			in:   typical + "\n\n",
			want: models.HostStats{
				Load1: 0.52, Load5: 0.58, Load15: 0.59,
				MemTotalMB: 1024, MemUsedMB: 512,
				NumCPU: 4, UptimeSec: 12345.67,
			},
		},
		{
			name: "empty output yields zero value",
			in:   "",
			want: models.HostStats{},
		},
		{
			name: "load and cpu only",
			in:   "1.00 2.00 3.00 4/50 678\n2\n",
			want: models.HostStats{Load1: 1, Load5: 2, Load15: 3, NumCPU: 2},
		},
		{
			name: "all memory available means zero used",
			in:   "MemTotal: 2048 kB\nMemAvailable: 2048 kB\n",
			want: models.HostStats{MemTotalMB: 2, MemUsedMB: 0},
		},
		{
			name: "memtotal without available leaves used zero",
			in:   "MemTotal:       1048576 kB\n",
			want: models.HostStats{MemTotalMB: 1024},
		},
		{
			name: "unparseable nproc is ignored",
			in:   "not-a-number\n",
			want: models.HostStats{},
		},
		{
			name: "three field line is ignored",
			in:   "total used free\n",
			want: models.HostStats{},
		},
		{
			name: "lines are processed in order so available after total",
			in:   "MemTotal: 4096 kB\nMemAvailable: 1024 kB\n",
			want: models.HostStats{MemTotalMB: 4, MemUsedMB: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseHostStats(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseHostStats() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseInspect(t *testing.T) {
	const sample = `[{
      "Id": "abc123",
      "Name": "/web-1",
      "Created": "2026-10-01T12:00:00.0Z",
      "Config": {"Image": "nginx:alpine"},
      "State": {"Status": "running", "RestartCount": 3},
      "Mounts": [
        {"Type": "bind", "Source": "/srv/www", "Destination": "/usr/share/nginx/html", "RW": false},
        {"Type": "volume", "Source": "www_data", "Destination": "/var/cache", "RW": true}
      ],
      "NetworkSettings": {
        "Networks": {
          "web_default": {"IPAddress": "172.20.0.5"},
          "no_ip_net":   {"IPAddress": ""}
        },
        "Ports": {
          "443/tcp": null,
          "80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]
        }
      }
    }]`
	d, err := parseInspect(sample)
	if err != nil {
		t.Fatalf("parseInspect: %v", err)
	}
	if d.Name != "web-1" || d.Image != "nginx:alpine" || d.Status != "running" || d.RestartCount != 3 {
		t.Errorf("scalar fields: %+v", d)
	}
	if len(d.Networks) != 1 || d.Networks[0].Name != "web_default" || d.Networks[0].IPAddress != "172.20.0.5" {
		t.Errorf("networks = %+v, want only web_default", d.Networks)
	}
	wantPorts := []string{"443/tcp -> (not published)", "80/tcp -> 0.0.0.0:8080"}
	if len(d.Ports) != 2 || d.Ports[0] != wantPorts[0] || d.Ports[1] != wantPorts[1] {
		t.Errorf("ports = %v, want %v (sorted)", d.Ports, wantPorts)
	}
	if len(d.Mounts) != 2 || d.Mounts[0] != "bind: /srv/www -> /usr/share/nginx/html (ro)" {
		t.Errorf("mounts = %v", d.Mounts)
	}
	if d.Mounts[1] != "volume: www_data -> /var/cache (rw)" {
		t.Errorf("mounts[1] = %q", d.Mounts[1])
	}
}

func TestParseInspectMalformed(t *testing.T) {
	for _, in := range []string{"", "[]", "not json", "{}"} {
		if _, err := parseInspect(in); err == nil {
			t.Errorf("parseInspect(%q) expected error", in)
		}
	}
}

func TestParseDF(t *testing.T) {
	const sample = `{"Type":"Images","TotalCount":"7","Active":"4","Size":"2.5GB","Reclaimable":"500MB (20%)"}` + "\n" +
		`{"Type":"Containers","TotalCount":"9","Active":"6","Size":"150MB","Reclaimable":"30MB (20%)"}` + "\n" +
		`{"Type":"Local Volumes","TotalCount":"3","Active":"2","Size":"1.1GB","Reclaimable":"0B (0%)"}` + "\n" +
		`{"Type":"Build Cache","TotalCount":"12","Active":"0","Size":"4.2GB","Reclaimable":"4.2GB"}` + "\n"
	got := parseDF(sample)
	if len(got) != 4 {
		t.Fatalf("rows=%d, want 4", len(got))
	}
	want := models.DiskObject{Type: "Images", Total: "7", Active: "4", Size: "2.5GB", Reclaimable: "500MB (20%)"}
	if got[0] != want {
		t.Errorf("row 0 = %+v, want %+v", got[0], want)
	}
	if got[3].Type != "Build Cache" || got[3].Reclaimable != "4.2GB" {
		t.Errorf("row 3 = %+v", got[3])
	}
}

func TestParseDFMalformed(t *testing.T) {
	in := "not json\n{\"Type\":\"Images\",\"Total\":\"1\"}\ngarbage\n\n"
	got := parseDF(in)
	if len(got) != 1 || got[0].Type != "Images" || got[0].Active != "" {
		t.Errorf("parseDF kept bad rows: %+v", got)
	}
	if res := parseDF(""); res != nil {
		t.Errorf("parseDF(\"\") = %+v, want nil", res)
	}
}

func TestParsePs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []models.Container
	}{
		{
			// docker ps reports the 12-char short id; that is the app's
			// canonical container id, so the graph joins on it unchanged.
			name: "short id kept, networks split and sorted",
			in:   `{"Id":"8f30766c1db2","Names":"odysseus-odysseus-1","Image":"odysseus","State":"running","Status":"Up 3 hours","Networks":"v2_default,airport_default,backend_default"}`,
			want: []models.Container{{ID: "8f30766c1db2", Names: "odysseus-odysseus-1", Image: "odysseus",
				State: "running", Status: "Up 3 hours",
				Networks: []string{"airport_default", "backend_default", "v2_default"}}},
		},
		{
			name: "default networks kept here",
			in:   `{"Id":"36f80c5ea5bd","Names":"teamcity-server","State":"running","Networks":"bridge"}`,
			want: []models.Container{{ID: "36f80c5ea5bd", Names: "teamcity-server", State: "running",
				Networks: []string{"bridge"}}},
		},
		{
			name: "absent or empty networks stay nil",
			in:   "{\"Id\":\"aaa111222333\",\"Names\":\"x\"}\n{\"Id\":\"bbb111222333\",\"Names\":\"y\",\"Networks\":\"\"}\n",
			want: []models.Container{{ID: "aaa111222333", Names: "x"}, {ID: "bbb111222333", Names: "y"}},
		},
		{
			name: "blank and garbage lines skipped",
			in:   "\nnot json\n" + `{"Id":"ccc111222333","Names":"z","Networks":"net_a"}` + "\n",
			want: []models.Container{{ID: "ccc111222333", Names: "z", Networks: []string{"net_a"}}},
		},
		{
			name: "empty output",
			in:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestShellCommand(t *testing.T) {
	tests := []struct {
		name string
		id   string
		term string
		want string
	}{
		{
			name: "plain id and term",
			id:   "abc123", term: "xterm-256color",
			want: `docker exec -i -t -e TERM='xterm-256color' 'abc123' sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec /bin/sh'`,
		},
		{
			name: "id with quote is escaped",
			id:   "we'ird", term: "dumb",
			want: `docker exec -i -t -e TERM='dumb' 'we'\''ird' sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec /bin/sh'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellCommand(tt.id, tt.term); got != tt.want {
				t.Errorf("ShellCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The sweep is one shell string run on a remote host, so its exact shape is the
// contract: only validated numbers outside quotes, and an exit status that
// survives a container disappearing mid-loop.
func TestErrSweepCommand(t *testing.T) {
	const want = "docker ps -a --format '{{.ID}} {{.Names}}' | while read -r id name; do " +
		"docker logs --timestamps --since '7200s' --tail 500 \"$id\" 2>&1 | " +
		"awk -v p=\"$id\" -v n=\"$name\" 'BEGIN{OFS=\"\\t\"}{print p,n,$0}' || true; done; exit 0"
	if got := ErrSweepCommand("7200s", 500); got != want {
		t.Errorf("ErrSweepCommand() = %q,\n want               %q", got, want)
	}

	// A hostile window can only reach the shell inside single quotes, and the
	// tail is an int: nothing in the command is an unquoted expansion.
	hostile := ErrSweepCommand(`7200s'; rm -rf /`, 0)
	if !strings.Contains(hostile, `'7200s'\''; rm -rf /'`) {
		t.Errorf("since not escaped: %q", hostile)
	}
	if !strings.Contains(hostile, "--tail 0 ") {
		t.Errorf("tail not a bare number: %q", hostile)
	}
	for _, frag := range []string{"|| true", "; exit 0", "2>&1", "--timestamps", `"$id"`} {
		if !strings.Contains(hostile, frag) {
			t.Errorf("command is missing %q: %q", frag, hostile)
		}
	}
}

// The peaks probe is one shell string run on a remote host, so its shape is the
// contract: the ids are separate quoted arguments and never part of the script.
func TestPeaksCommand(t *testing.T) {
	ids := []string{"8f30766c1db2", "4bc22d376cec"}
	got := PeaksCommand(ids)
	if got != PeaksCommand(ids) {
		t.Error("command is not byte-stable for the same ids")
	}
	if !strings.HasPrefix(got, "sh -c '") || !strings.HasSuffix(got, "' sh '8f30766c1db2' '4bc22d376cec'") {
		t.Errorf("unexpected command shape: %q", got)
	}
	// The script carries no single quotes, so quote() adds no escapes and the
	// ids stay the only quoted arguments.
	if strings.Contains(peaksScript, "'") {
		t.Error("peaksScript contains a single quote; quote() would have to escape it")
	}
	// A hostile id is dropped, not escaped into the argument list.
	one := PeaksCommand([]string{"8f30766c1db2"})
	hostile := PeaksCommand(validPeakIDs([]string{"8f30766c1db2", "x'; rm -rf /", "$(reboot)", "TOO-SHORT"}))
	if hostile != one {
		t.Errorf("invalid ids changed the command: %q", hostile)
	}
	for _, frag := range []string{"docker inspect", "memory.peak", "memory.max_usage_in_bytes",
		"usage_usec", "cpuacct.usage", "nr_throttled", `printf "%s\t%s`, "-d\" \"", "for id in \"$@\""} {
		if !strings.Contains(got, frag) {
			t.Errorf("command is missing %q", frag)
		}
	}
	if len(validPeakIDs(nil)) != 0 {
		t.Error("no ids must mean no probe")
	}
}

func TestParseContainerPeaks(t *testing.T) {
	// Real rows, as this machine's cgroup v2 emits them.
	v2 := "8f30766c1db2\t1852186624\t3768589852\t0\t0\t2026-10-10T20:03:29.692717076Z\n"
	got := parseContainerPeaks(v2)
	if len(got) != 1 {
		t.Fatalf("rows = %d; want 1", len(got))
	}
	want := models.ContainerPeak{
		ID: "8f30766c1db2", MemPeakBytes: 1852186624, CPUUsec: 3768589852,
		Throttled: 0, Periods: 0, StartedAt: "2026-10-10T20:03:29.692717076Z",
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("row = %+v,\nwant %+v", got[0], want)
	}

	// A host with nothing readable reports every counter as unknown. Zero is a
	// measurement — 0 throttled periods is a fact, "-" is not.
	none := parseContainerPeaks("8f30766c1db2\t-\t-\t-\t-\t-\n")
	if none[0].MemPeakBytes != models.Unknown || none[0].CPUUsec != models.Unknown ||
		none[0].Throttled != models.Unknown || none[0].Periods != models.Unknown {
		t.Errorf("dash row parsed as %+v", none[0])
	}
	if none[0].StartedAt != "" {
		t.Errorf("start time invented: %q", none[0].StartedAt)
	}

	// cgroup v1 gives nanoseconds, which the script already scaled; garbage in a
	// field makes only that field unknown.
	mixed := parseContainerPeaks("054a4270e27b\t82149376\tnotanumber\t12\t4000\t2026-10-10T20:03:29Z\n")
	if mixed[0].MemPeakBytes != 82149376 || mixed[0].CPUUsec != models.Unknown ||
		mixed[0].Throttled != 12 || mixed[0].Periods != 4000 {
		t.Errorf("mixed row parsed as %+v", mixed[0])
	}

	for _, bad := range []string{"", "\n", "not-a-row\n", "zzzzzzzzzzzz\t1\t2\t3\t4\t5\n",
		"8f30766c1db2\t1\t2\t3\n", "shell noise: cat: not found\n"} {
		if rows := parseContainerPeaks(bad); len(rows) != 0 {
			t.Errorf("parseContainerPeaks(%q) returned %d rows", bad, len(rows))
		}
	}
}

// The snapshot is one shell string, so its exact shape is the contract: the
// two \x1e separators delimit the three sections and `docker ps`'s exit code
// is the shell's, while `docker stats` may fail without sinking the batch.
func TestSnapshotCommand(t *testing.T) {
	const want = `sh -c 'docker ps -a --format "{{json .}}"; rc=$?; printf "\036"; ` +
		`docker stats --no-stream --format "{{json .}}" || true; printf "\036"; ` +
		`cat /proc/loadavg; cat /proc/uptime; grep -E "^Mem(Total|Available):" /proc/meminfo; nproc; exit $rc'`
	if snapshotCmd != want {
		t.Errorf("snapshotCmd = %q,\n want         %q", snapshotCmd, want)
	}
	for _, frag := range []string{`--format "{{json .}}"`, "--no-stream", "|| true", "exit $rc",
		"/proc/loadavg", "/proc/uptime", "Mem(Total|Available)", "nproc"} {
		if !strings.Contains(snapshotCmd, frag) {
			t.Errorf("command is missing %q", frag)
		}
	}
}

// parseSnapshot is Snapshot's whole parsing half against a canned payload.

func TestSnapshotParsesThreeSections(t *testing.T) {
	ps := `{"Id":"8f30766c1db2","Names":"web","Image":"nginx","State":"running","Status":"Up 2 hours","CreatedAt":"2026-10-10 20:03:28 +0000 UTC","Labels":"","Networks":"bridge"}` + "\n"
	stats := `{"Id":"8f30766c1db2","Name":"web","CPUPerc":"0.15%","MemUsage":"12MiB / 1.9GiB","MemPerc":"0.61%"}` + "\n"
	host := "0.10 0.08 0.05 1/234 5678\n12345.67 23456.78\nMemTotal:  2019460 kB\nMemAvailable:  1512000 kB\n4\n"
	payload := ps + "\x1e" + stats + "\x1e" + host

	containers, cstats, hs, err := parseSnapshot(payload)
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "8f30766c1db2" || containers[0].Names != "web" {
		t.Errorf("containers = %+v", containers)
	}
	if len(cstats) != 1 || cstats[0].ID != "8f30766c1db2" || cstats[0].CPUPerc != "0.15%" {
		t.Errorf("cstats = %+v", cstats)
	}
	if hs.NumCPU != 4 || hs.Load1 != 0.10 || hs.UptimeSec != 12345.67 || hs.MemTotalMB != 1972 || hs.MemUsedMB != 1972-1476 {
		t.Errorf("host stats = %+v", hs)
	}

	// An empty stats section (docker stats failed under `|| true`) yields no
	// rows, not an error.
	if _, cstats, _, err := parseSnapshot(ps + "\x1e\x1e" + host); err != nil || len(cstats) != 0 {
		t.Errorf("empty stats section: cstats=%d err=%v", len(cstats), err)
	}

	// Fewer or more sections is a protocol breach, not silently swapped data.
	for name, bad := range map[string]string{
		"two sections":  ps + "\x1e" + host,
		"four sections": ps + "\x1e" + stats + "\x1e" + host + "\x1eextra",
		"no separator":  ps + stats + host,
	} {
		if _, _, _, err := parseSnapshot(bad); err == nil || !strings.Contains(err.Error(), "expected 3 sections") {
			t.Errorf("%s: err = %v, want section-count error", name, err)
		}
	}
}
