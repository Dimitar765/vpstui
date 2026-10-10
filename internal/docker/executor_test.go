package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"vpstui/internal/models"
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
