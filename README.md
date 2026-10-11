# vpstui

A terminal UI (TUI) for managing and monitoring Docker containers on remote
VPS servers over SSH, written in Go.

## Features

- **Host manager** — add/edit/delete VPS entries, persisted to
  `~/.config/vpstui/hosts.json` (mode 0600, atomic writes). A corrupt
  hosts or tags file is renamed aside to `<file>.bad` on load and never
  overwritten, so your data survives a bad edit.
- **SSH auth** — password, private key (optional passphrase), or ssh-agent.
  First-connect trust-on-first-use into `~/.config/vpstui/known_hosts`;
  a changed host key blocks the connection.
- **Dashboard** — live host metrics (load, memory, CPUs, uptime) and a
  container table (state, status, CPU %, MEM %) refreshed every 2s, all
  computed by one batched `docker ps` / `docker stats --no-stream` exec over
  SSH. Columns adapt to the terminal width; running/exited/other states are
  color-coded and high CPU/MEM readings highlight as they climb. If the SSH
  connection drops, vpstui retries once automatically after ~5s; if that also
  fails it shows the "connection lost — press c to reconnect" banner and `c`
  redials by hand.
- **Container actions** — start (`s`), stop (`x`), restart (`r`), remove (`D`,
  stopped containers only) with `y/N` confirmation. Containers are grouped by
  compose project (fold with `space`), details via `d`.
- **Interactive shell** — `e` on a running container suspends the UI and
  opens a shell inside it (`docker exec -i -t`, bash with `/bin/sh`
  fallback) over the same SSH connection; `exit` resumes the dashboard.
- **Disk panel** — `i` shows `docker system df`; `p`/`P` prune images/system
  after confirmation.
- **Performance screen** — `p` ranks every container by how much of the machine it
  takes, so the noisy neighbour is row 1 instead of a guess. vpstui keeps a
  30-minute history of readings per container (the 2s tick while you watch that
  host, a 10s stats-only sample in the background for every other connected one)
  and shows the last 20 minutes' peak and mean beside the live figure, plus a
  sparkline. Two badges carry the judgement: `HOG` for a container holding ≥10 %
  of the box — CPU against the total core count, or its slice of host RAM — and
  `SPIKE` for one now at ≥3× its own window mean. `f` asks the kernel for the
  *all-time* peak (`memory.peak`, cumulative `cpu.stat`), which answers what
  happened while nobody was watching. See [Performance](#performance).
- **Log streaming** — follow `docker logs -f` in a scrollable viewport
  (`enter` from the dashboard, `esc` to stop). Lines are colour-coded by level
  (`ERROR` red, `WARNING` amber, `trace`/`debug` dim) with a dimmed
  `HH:MM:SS` timestamp; a container that ships its own colours keeps them.
  `/` filters the buffer live on a case-insensitive substring, `m` toggles
  between hiding non-matches and bolding them, `n`/`N` jump between matches and
  `esc` clears. Scrolling up pauses follow until `G`, with a `↓ N new` badge for
  what you missed.
- **Fleet error aggregation** — `a` sweeps the recent logs of *every* container
  (including exited ones) on the hosts you have opened, keeps error, warning and
  failure lines (tracebacks, `panic`, `Killed`, OOM, timeouts) and normalises them
  into signatures — ids, hex runs, digits, paths and quoted strings become
  placeholders — so one problem repeated 400 times reads as one row counted 400.
  `enter` expands a signature into its hosts, containers and raw samples, `l`
  opens that container's live stream, `m` switches grouped/flat, `w` narrows to
  errors only, `r` re-sweeps every connected host. Sweeps run on connect, on `f`
  and on `r` — never on the 2s tick — so a host you have not opened contributes
  nothing, and the status line says how many are covered (`2 of 5 hosts swept`).
- **Container tags** — `t` on a container gives it an alias and one of six
  colours (`sky`, `green`, `amber`, `rose`, `violet`, `slate`); `T` drops the
  label. Nothing reaches docker — the container keeps its own name everywhere and
  `docker ps` is untouched — a tag is vpstui's own opinion, kept in
  `~/.config/vpstui/tags.json` and shown beside the name in the dashboard table,
  the log title, the error screen and the web view. Labels are keyed by *host +
  container name*, so they survive `docker rm` and `compose up -d` (which mints a
  fresh id every time), and the same name on two hosts is two labels. Reusing a
  name for a different container moves the label onto it — that is what a human
  label means; the id it was written for is recorded alongside, and nothing
  expires on its own.
- **Yank errors** — `y` copies the line under the cursor, `Y` the whole signature:
  a header for the human reading the paste, then raw docker lines for `grep` on
  the host. It needs a clipboard helper on this machine (`wl-copy`, `xclip`,
  `xsel` or `pbcopy`) and says so when there is none. The status line reports
  counts only — the yanked text came off a remote container, so it is not put
  back on screen as though it were our own message.
- **Local JSON API + web view** — the running TUI publishes live session data
  on `http://127.0.0.1:8712`, with a bundled zero-build dashboard at `/`
  (Sessions, Performance, Network and Errors tabs). See [Web API](#web-api).

## Run

```sh
go build -o vpstui .
./vpstui
```

Keys are shown in the help bar of each screen; `ctrl+c` quits from anywhere.
Vim-style navigation works throughout: `j`/`k` move rows or scroll, `g`/`G`
jump to top/bottom, and help bars are pinned to the last terminal row. The log
screen adds `/` to filter, `m` to switch between hiding and highlighting, and
`n`/`N` to step through matches; `tab`, `shift+tab`, the digits and `X` keep
switching sessions, except while the filter is being typed (then the keys are
the query's). From the host list or the dashboard, `a` opens the fleet-wide error
screen; because it aggregates every host, the session-switching keys are inert
there. `t` tags the container under the cursor and `T` untags it (dashboard and
error screen alike); inside the tag prompt `tab` cycles the colour palette and
`enter`/`esc` commit or discard, and those keys belong to the prompt while it is
open. On the error screen `y` yanks the line under the cursor and `Y` the whole
signature. On the dashboard `p` opens the perf screen: `j`/`k`/`g`/`G` to move,
`enter` for that container's logs, `t`/`T` to tag or untag it, `f` to re-probe the
all-time peaks, `esc` to close. Its rows re-rank every sample, so the cursor stays
on the container it was placed on rather than on a row number. (`p`/`P` mean prune
only inside the disk panel, which `i` opens.)

## Managing this machine

vpstui has one transport: it dials an SSH server and runs `docker`, `awk` and
`ps` **there**. The client is its own, not `ssh(1)`, so `~/.ssh/config` is never
read — no aliases, no `ProxyJump`, no `IdentityFile` — and a host entry carries
address, port, user and key path explicitly. The machine vpstui runs on is
therefore an ordinary target as soon as something listens for SSH on it.

Give vpstui its own keypair, without a passphrase, so it can dial unattended:

```sh
ssh-keygen -t ed25519 -N '' -C "vpstui@$(hostname)" -f ~/.ssh/vpstui_local
mkdir -p ~/.ssh && chmod 700 ~/.ssh
printf 'from="127.0.0.1",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc ' \
  >> ~/.ssh/authorized_keys
cat ~/.ssh/vpstui_local.pub >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
```

A dedicated key — rather than a real identity key — means the whole feature is
removed by deleting two files and one `authorized_keys` line. The restrictions
make the key useless to any client that is not on loopback, and `no-user-rc`
keeps login scripts from writing into parsed output; `command=` is deliberately
absent because vpstui runs many different commands, and `no-pty` would break the
`e` shell.

Then point it at a server. Either bind the system sshd to loopback with a
drop-in, so no LAN or WAN SSH surface appears on a machine that deliberately had
none:

```sh
sudo tee /etc/ssh/sshd_config.d/90-loopback.conf >/dev/null <<'EOF'
ListenAddress 127.0.0.1
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
EOF
sudo systemctl start sshd       # add `enable` to survive a reboot
ss -ltn '( sport = :22 )'       # must show 127.0.0.1:22 and nothing else
```

…or run your own sshd on a high port, which needs no sudo and restarts only when
you re-run it:

```sh
D=~/.config/vpstui/sshd-local; mkdir -p "$D" && chmod 700 "$D"
ssh-keygen -q -t ed25519 -N '' -f "$D/host_ed25519"   # host key, not the login key
cat > "$D/sshd_config" <<EOF
Port 2222
ListenAddress 127.0.0.1
HostKey $D/host_ed25519
AuthorizedKeysFile $HOME/.ssh/authorized_keys
PidFile $D/sshd.pid
UsePAM no
PasswordAuthentication no
PermitRootLogin no
StrictModes no
EOF
chmod 600 "$D/sshd_config"
setsid /usr/bin/sshd -f "$D/sshd_config" -D -E "$D/log" &
```

Every path in that config has to be absolute — sshd does not expand `~`. After a
reboot the same `setsid` line brings it back, and because the host key lives in
`$D` vpstui's `known_hosts` record still matches.

A non-root sshd logs `setgroups() failed: Operation not permitted` and still
inherits the launcher's supplementary groups, so the `docker` group keeps
working. Either way, check the channel before blaming the app:

```sh
ssh -i ~/.ssh/vpstui_local -o BatchMode=yes echo@127.0.0.1 -p 2222 'echo ok'  # must print only ok
```

Anything else on that line — a motd, a `.zshenv` echo, an interactive-only
prompt — lands in front of every parsed `docker` output.

Add the entry with `n`: address `127.0.0.1`, port `22` (or `2222`), your own
user rather than `root`, auth `key`, key path `~/.ssh/vpstui_local`. The host
key is trusted on first use into `~/.config/vpstui/known_hosts`, and a later
change blocks the connection exactly as it would for a VPS.

What this costs you is worth stating plainly: nothing is sandboxed. On this
host, `x`, `D`, `p` and `P` act on your **real** development containers, because
there is no second daemon behind the entry. If you would rather not run an sshd
on the host at all, put one in a container with `/var/run/docker.sock` mounted
and publish it on `127.0.0.1:<port>` — vpstui sees an ordinary SSH target, and
deleting the container deletes the surface.

To undo: `sudo systemctl disable --now sshd` and
`sudo rm /etc/ssh/sshd_config.d/90-loopback.conf` — or, for the private sshd,
`kill "$(cat ~/.config/vpstui/sshd-local/sshd.pid)"` and delete that directory.
Then `rm ~/.ssh/vpstui_local*` and drop the one `authorized_keys` line.

## Web API

While the TUI runs it also serves a read-only JSON API on loopback, sharing the
same data the dashboard shows. Handlers read from an in-memory snapshot that the
UI republishes on its existing 2s tick, so the API costs no extra SSH traffic —
and it is only alive for as long as the TUI is.

Open `http://127.0.0.1:8712/` for the bundled dashboard (one static file, no
build step, same origin so there are no CORS issues). It lists every published
session with host metrics and a container table, re-reading the API every 5s by
default. The **refresh** control in the header offers 2s–60s and remembers your
choice; panels only repaint when the data actually changes, so a quiet host
stays perfectly still.

| Endpoint | Returns |
| --- | --- |
| `GET /healthz` | `{"ok":true}` |
| `GET /api/sessions` | `[{id, name, target, connected, fetchedAt, containerCount}]` |
| `GET /api/sessions/{id}` | host stats, per-container rows and the perf ranking |
| `GET /api/sessions/{id}/network` | container↔network graph |
| `GET /api/errors` | cross-host error groups and sweep coverage |

```json
{
  "id": "a1b2c3", "name": "web", "target": "root@203.0.113.10:22",
  "connected": true, "fetchedAt": "2026-10-10T20:48:46Z",
  "hasHostStats": true,
  "hostStats": { "load1": 0.12, "load5": 0.08, "load15": 0.06,
                 "memTotalMB": 1969, "memUsedMB": 612, "numCPU": 2, "uptimeSec": 84123.5 },
  "containers": [
    { "id": "9f2c1a7b", "name": "odysseus-postfix-1", "tag": "mail", "tagColor": "sky",
      "image": "nginx:1.27", "state": "running",
      "status": "Up 3 hours", "cpu": "0.7%", "memPerc": "2.1%",
      "memUsage": "41.2MiB / 1.9GiB", "cpuPct": 0.7, "memPct": 2.1 }
  ]
}
```

`cpu`/`memPerc`/`memUsage` are docker's pre-formatted strings (`"-"` before the
first sample lands); `cpuPct`/`memPct` are the same values parsed to numbers, or
`null` when unparseable. `tag`/`tagColor` are vpstui's label for that container,
empty when it has none. Unknown ids return `404`, non-`GET` requests `405`, and
requests after the TUI has started quitting return `503`.

Tags are authored in the TUI and **read-only** in the browser: the page renders
the label in its palette colour next to the docker name, in both the table and
the graph, but never edits them. The values come from the same snapshot publish
as everything else, so the TUI and the page cannot disagree — and a tag change
reaches the page on the next publish without waiting for a resweep.

### Network tab

The **Network** tab (`/#network` deep-links it) draws each host as a bipartite
graph: containers *and* the user-defined docker networks they share, one edge
per membership, so five containers on one network draw five lines instead of
ten. The default networks (`bridge`, `host`, `none`, `ingress`) are not drawn —
every container is attached to `bridge`, which would connect everything to
everything — so containers on no user-defined network collect in a dim strip
below the clusters instead.

Membership is read from the same `docker ps` snapshot that fills the table, so the
graph costs no extra SSH round trip and ages exactly like the session — a container
and its networks always arrive in the same refresh.

```json
{
  "id": "a1b2c3", "available": true, "fetchedAt": "2026-09-03T14:20:00Z",
  "nodes": [
    { "id": "n:web_default", "kind": "network", "name": "web_default" },
    { "id": "c:9f2c1a7b", "kind": "container", "name": "odysseus-postfix-1", "tag": "mail", "tagColor": "sky", "state": "running" }
  ],
  "edges": [ { "from": "c:9f2c1a7b", "to": "n:web_default" } ],
  "isolated": [ { "id": "c:4d8e0f11", "kind": "container", "name": "cron", "state": "exited" } ]
}
```

`available` is `false` (with empty lists) until the first container list lands.
A container node is labelled with its tag when it has one, in the palette colour,
with the docker name kept in the node's `title` so nothing is lost.
Node ids are stable — `c:` + container id, `n:` + network name — and every list
is ordered, so an unchanged graph serialises identically and the page skips the
repaint. `truncated` marks a host whose graph was cut at 200 nodes.

### Errors tab

The **Errors** tab (`/#errors`) shows the same aggregation as the TUI screen: one
row per signature, worst-first by line count then by signature, so a tie never
shuffles between polls. Clicking a row expands its containers and the raw samples
behind it. Each row and each expand block carries a **copy** button: the row
yanks that one line, the block yanks the whole signature — the same two texts the
TUI's `y` and `Y` put on the clipboard, byte for byte.

```json
{
  "swept": 2, "connected": 3, "truncated": false,
  "fetchedAt": "2026-10-11T01:49:12Z", "lineCount": 75,
  "hosts": [
    { "id": "a1b2c3", "name": "web", "swept": true,
      "sweptAt": "2026-10-11T01:48:46Z", "lineCount": 71 },
    { "id": "d4e5f6", "name": "build", "swept": false,
      "sweptAt": "0001-01-01T00:00:00Z", "lineCount": 0 }
  ],
  "groups": [
    { "sig": "error payment provider timeout worker={hex}", "sev": "error",
      "count": 4, "hosts": ["web"], "containers": ["web/odysseus-postfix-1"],
      "firstSeen": "2026-10-11T01:41:02Z", "lastSeen": "2026-10-11T01:48:30Z",
      "sample": { "host": "web", "container": "odysseus-postfix-1", "tag": "mail",
                  "sev": "error", "stamp": "01:48:30",
                  "text": "error payment provider timeout worker=3f9c1a7b2d8e" },
      "examples": [ { "host": "web", "container": "odysseus-postfix-1", "tag": "mail",
                      "sev": "warn", "stamp": "01:41:02",
                      "text": "warning payment provider timeout worker=3f9c1a7b2d8e" },
                    { "…": "the newest 20 lines, oldest first" } ]
    }
  ]
}
```

`examples` is the newest 20 lines behind the signature, oldest first, so a pasted
group reads like a log and its last entry is exactly the `sample` the row shows.
That pick lives in one place (`internal/errsample`), shared by the TUI and this
payload — which is why `Y` and the page's copy button agree byte for byte.
`sample.sev` and each example's `sev` are that **line's** severity, not the
group's: a group is graded by the worst thing its signature ever said, so a warn
line can be the newest sample of an error group. `containers` keys stay
`host/dockerName` — that string is the dedupe identity — while their display and
the copy text use your tag where one is set.

`swept` versus `connected` is the coverage line: `hosts` lists every session the
TUI has published, and one not yet swept shows `swept: false` with a zero
`sweptAt`. A host you have never connected to is not published at all, so it is
absent even from the coverage count — the screen reports `2 of 5 hosts swept`
against the host list, while the API can only speak for sessions that exist.
`hosts[].sweptAt` is per host, `lineCount` counts kept lines (before grouping),
and `truncated` flags a payload cut at the byte or group cap. `fetchedAt` is the
newest sweep in the set.

The grouping lives in one place (`internal/app/errors.go`) and the TUI does it
before publishing, so the screen and the endpoint always agree — and the handlers
still read only the in-memory snapshot, never SSH. Only **visited** hosts appear:
a sweep runs when a session connects, on `f`, and on `r` from the error screen,
never on the 2s tick, so `/api/errors` ages exactly like the last sweep rather
than like the page's own polling.

### Performance tab

The **Performance** tab (`/#perf`) prints the same ranking as the TUI screen, in the
same order: `perf.rows` arrives already ranked, so the page sorts nothing and cannot
crown a different winner than the terminal did. Each host panel states its own
denominators — core count, host RAM, the window, the retention, how many samples sit
behind the numbers and when the all-time probe last ran — because a peak without a
stated window is not a claim.

```json
{
  "perf": {
    "windowSec": 1200, "retentionSec": 1800, "samples": 3096,
    "probed": true, "probedAt": "2026-10-11T07:54:51Z", "supported": true,
    "rows": [
      { "id": "6c2f36fa9cae", "name": "worker", "cpu": 293.5, "cpuAvg": 294.5,
        "cpuPeak": 298.9, "share": 24.5, "spike": 1.0, "flags": ["hog"],
        "samples": 344, "memBytes": 1032192, "memPeakBytes": 1048576,
        "allTimeMemPeakBytes": 7995392, "cpuSeconds": 186.4, "cpuAvgCores": 0.26,
        "throttled": 0, "periods": 0, "series": [293.1, 294.8, 296.0] }
    ]
  }
}
```

`share` is a percentage of the box (0–100) — the same rounded figure the `SHARE` cell
shows — and `flags` holds `hog` and `spike` exactly as the terminal badges them.
`series` is the window already reduced to at most 80 points, which is all the
sparkline can draw; `samples` is the true reading count behind it, so a short series
never reads as a short history. `cpuAvgCores` is `cpuSeconds` divided by the
container's own lifetime — Go does that division, and the page prints the result in
the `cpu s` cell's hover text rather than recomputing it. The all-time fields are
**absent**, not zero,
when the host exposes no cgroup peak or the container was not in the last probe, and
`supported: false` says the host has no counters at all. `perf` itself is omitted
until a container has its first sample. The **Sessions** tab carries nothing but the
badge for each row, joined by container id, so the two tabs cannot disagree about
which container is the problem.

Two behaviours worth knowing: the full refresh — `docker ps`, `docker stats` and the
host metrics together — runs only for the **active** session, so a background host's
table and graph freeze at their last refresh. The perf sample is the exception: every
connected session takes a stats-only reading every 10s and republishes, so a
background session's `fetchedAt` and its `perf` rows keep advancing whether or not you
open it — which is what makes the ranking worth reading from the page. Poll it or
compare the timestamp. And the server binds loopback only;
`VPSTUI_HTTP_ADDR` can change the port but a non-loopback address is refused, and
a busy port just prints a warning to stderr and leaves the TUI working.

`VPSTUI_LOG_TAIL` sets how many lines `docker logs --tail` replays when a log
screen opens — default 2000, clamped to 1..100000, ignored if unparseable. The
view then keeps up to 8 MiB (or 100k lines) of history, dropping whole older
lines behind a `… (older lines dropped) …` marker.

The error sweep has its own two bounds, applied per container by `docker logs`:
`VPSTUI_ERR_SINCE` (default `2h`; a Go duration, with `d`/`w` understood, clamped
to 7 days) and `VPSTUI_ERR_TAIL` (default 500, clamped to 1..5000). Unparseable
values fall back to the defaults rather than asking the daemon for history. What
survives classification is then capped per host: 4 MiB of payload (cut at a line
boundary and flagged `truncated`), 500 newest lines of corpus, 20 samples per
group, the newest 1000 rows in the flat feed, and 200 groups published to
`/api/errors`.

Signatures are deliberately aggressive: two lines that differ only in an id, a
number, a path or a quoted payload collapse into one row. The cost is that a JSON
log line whose field names vary can normalise to something like
`{str}:{str},{str}` — which is why every group keeps its raw samples and the flat
feed always shows the original line, stamp and ANSI stripped but otherwise
untouched.

## Performance

Every number the dashboard shows about load is **instantaneous**: `docker stats
--no-stream` answers "right now" and the next refresh overwrites it. That cannot
answer the two questions a shared box actually raises — *what was the peak?* and *who
is eating the machine?* Both need a window and a denominator, so vpstui keeps them,
and `p` opens the screen that shows them.

**Sampling.** Each reading is recorded per container **id**, never per name: names get
reused, and a rebuilt container must not inherit a dead one's history. The host on
screen samples on its existing 2s tick, which costs no extra SSH at all. Every other
connected host samples `docker stats --no-stream` alone every 10s — one round trip per
10s, against the three every 2s an open dashboard makes. The buffer holds 900 samples
per container over a **30-minute horizon**, whichever bound is reached first.

**The window.** Peak, mean and the sparkline are measured over the last **20 minutes**,
and the header states that window beside the core count and host RAM every share is
computed against:

```
perf web · 12 cpu · 23.3GiB · window 20m · 3096 samples · probed 07:54:51
#  NAME              CPU%   AVG   PEAK   TREND               SHARE  MEM      M.PEAK   THROT  FLAG
1  worker            293.5  294.5 298.9  ▇▇▇█▇▇▇▇▇▇▇▇▇▇▇▇▇▇  24.5%  1008KiB  7.63MiB  0      HOG
```

Rows come out ranked by share, largest first, computed in Go — the renderer sorts
nothing, so the terminal and the web tab show the same order and the same winner.
Because a live ranking reshuffles rows every sample, the cursor is anchored to the
**container** rather than to a row index: put it on `db`, let `worker` overtake it, and
the cursor stays on `db`. `enter` opens that container's logs, `t`/`T` tag and untag it
like anywhere else, `esc` closes.

**What makes a hog.** Two share-of-the-box thresholds, because "100 % CPU" is one core
here and the entire machine on a single-vCPU VPS:

| Constant | Value | Meaning |
| --- | --- | --- |
| `HogShareCPU` | 0.10 | the container's CPU is ≥10 % of the box's **total** cores |
| `HogShareMem` | 0.10 | …or it holds ≥10 % of host RAM |
| `SpikeRatio` | 3.0 | …or it now reads ≥3× its own mean over the window |
| `SpikeFloor` | 10.0 | …and only above 10 % absolute, so 0.1 %→1.1 % is not a "spike" |
| `MinSamples` | 8 | …and only once ≥8 samples exist, so a cold start is not a spike |
| `Window` | 20m | the window peak, mean and share are measured over |

A lone `sha1sum` on a 12-core box is 8 % of the machine and earns no badge; the same
process jumping from 2 % to 20 % is a `SPIKE`; a container holding 3 GiB of a 4 GiB
VPS is a `HOG` on memory alone. The constants are exported from `internal/perf`, so
they can be argued with in one place rather than in three renderers.

**The all-time peak.** Our window only knows what it was watching. The kernel knows
more: cgroup v2 keeps `memory.peak` (the highest usage since the container started)
and cumulative `cpu.stat usage_usec`, and cgroup v1 has `memory.max_usage_in_bytes`
and `cpuacct.usage`. `f` runs one read-only probe over SSH that globs each container's
cgroup scope directory and reports those counters: `M.PEAK` is then the peak from
*while you were away*, and `CPU S` is the container's cumulative CPU time since it
started — 64 minutes of burn, not 64 minutes of the window you are looking at. A total
is not a cost until it is a rate, so vpstui also divides it by the container's own
lifetime: a week-old container at 0.03 average cores was never the problem, one at
1.9 cores has been eating a core since boot (the web tab puts that rate in the
`cpu s` cell's hover text). A container idling at 428 KiB with a 154 MiB all-time peak
is exactly the case a 20-minute window cannot see.

A host with no readable cgroup is not guessed at: the all-time columns print `-` and
the header says `web: cgroup peak not available`. In those columns `-` means
*unknown* and `0` means *measured* — the two are never conflated.

**History lives in memory.** The buffer belongs to the session, so it dies with the
connection and is never written to disk — restart vpstui and every window starts
empty again. That is deliberate: a rolling 30-minute view is a diagnostic, not an
archive, and an archive would need a retention policy, a directory and a story about
whose disk it eats.

## Secret storage

Passwords and key passphrases are stored in the OS keyring
(service `vpstui`) when available, otherwise in
`~/.config/vpstui/secrets.json` (mode 0600, plaintext). Private keys
themselves are never copied — only their path is saved. Password auth over
the network is weaker than keys; prefer ssh-agent or key auth.

## Requirements

Targets must run an SSH server and Docker (`docker` in PATH, user allowed to
access the daemon). No agent or daemon-side component is installed. The machine
vpstui itself runs on qualifies the same way, over loopback SSH — see
[Managing this machine](#managing-this-machine). The error
sweep additionally shells out to `awk` and `ps` (both standard on any Docker
host); if one is missing the error screen shows that host's shell error
(`host: sh: awk: command not found`) rather than silently returning zero errors.
The all-time peak probe additionally reads each container's cgroup files
(`memory.peak` and `cpu.stat` on v2, `memory.max_usage_in_bytes` and `cpuacct.usage`
on v1) as your SSH user; where none of them can be read the window columns still work
and the all-time columns report `-` beside a note saying so.
