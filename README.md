# vpstui

A terminal UI (TUI) for managing and monitoring Docker containers on remote
VPS servers over SSH, written in Go.

## Features

- **Host manager** — add/edit/delete VPS entries, persisted to
  `~/.config/vpstui/hosts.json` (mode 0600, atomic writes).
- **SSH auth** — password, private key (optional passphrase), or ssh-agent.
  First-connect trust-on-first-use into `~/.config/vpstui/known_hosts`;
  a changed host key blocks the connection.
- **Dashboard** — live host metrics (load, memory, CPUs, uptime) and a
  container table (state, status, CPU %, MEM %) refreshed every 2s, all
  computed by running `docker ps` / `docker stats --no-stream` over SSH.
  Columns adapt to the terminal width; running/exited/other states are
  color-coded and high CPU/MEM readings highlight as they climb.
- **Container actions** — start (`s`), stop (`x`), restart (`r`), remove (`D`,
  stopped containers only) with `y/N` confirmation. Containers are grouped by
  compose project (fold with `space`), details via `d`.
- **Interactive shell** — `e` on a running container suspends the UI and
  opens a shell inside it (`docker exec -i -t`, bash with `/bin/sh`
  fallback) over the same SSH connection; `exit` resumes the dashboard.
- **Disk panel** — `i` shows `docker system df`; `p`/`P` prune images/system
  after confirmation.
- **Log streaming** — follow `docker logs -f` in a scrollable viewport
  (`enter` from the dashboard, `esc` to stop). Lines are colour-coded by level
  (`ERROR` red, `WARNING` amber, `trace`/`debug` dim) with a dimmed
  `HH:MM:SS` timestamp; a container that ships its own colours keeps them.
  `/` filters the buffer live on a case-insensitive substring, `m` toggles
  between hiding non-matches and bolding them, `n`/`N` jump between matches and
  `esc` clears. Scrolling up pauses follow until `G`, with a `↓ N new` badge for
  what you missed.
- **Local JSON API + web view** — the running TUI publishes live session data
  on `http://127.0.0.1:8712`, with a bundled zero-build dashboard at `/`
  (Sessions and Network tabs). See [Web API](#web-api).

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
the query's).

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
| `GET /api/sessions/{id}` | host stats plus per-container rows |
| `GET /api/sessions/{id}/network` | container↔network graph |

```json
{
  "id": "a1b2c3", "name": "web", "target": "root@203.0.113.10:22",
  "connected": true, "fetchedAt": "2026-10-10T20:48:46Z",
  "hasHostStats": true,
  "hostStats": { "load1": 0.12, "load5": 0.08, "load15": 0.06,
                 "memTotalMB": 1969, "memUsedMB": 612, "numCPU": 2, "uptimeSec": 84123.5 },
  "containers": [
    { "id": "9f2c1a7b", "name": "api", "image": "nginx:1.27", "state": "running",
      "status": "Up 3 hours", "cpu": "0.7%", "memPerc": "2.1%",
      "memUsage": "41.2MiB / 1.9GiB", "cpuPct": 0.7, "memPct": 2.1 }
  ]
}
```

`cpu`/`memPerc`/`memUsage` are docker's pre-formatted strings (`"-"` before the
first sample lands); `cpuPct`/`memPct` are the same values parsed to numbers, or
`null` when unparseable. Unknown ids return `404`, non-`GET` requests `405`, and
requests after the TUI has started quitting return `503`.

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
    { "id": "c:9f2c1a7b", "kind": "container", "name": "api", "state": "running" }
  ],
  "edges": [ { "from": "c:9f2c1a7b", "to": "n:web_default" } ],
  "isolated": [ { "id": "c:4d8e0f11", "kind": "container", "name": "cron", "state": "exited" } ]
}
```

`available` is `false` (with empty lists) until the first container list lands.
Node ids are stable — `c:` + container id, `n:` + network name — and every list
is ordered, so an unchanged graph serialises identically and the page skips the
repaint. `truncated` marks a host whose graph was cut at 200 nodes.

Two behaviours worth knowing: only the **active** session ticks, so a background
session's `fetchedAt` freezes at its last refresh — its table and its graph both
do. Poll it or compare the timestamp. And the server binds loopback only;
`VPSTUI_HTTP_ADDR` can change the port but a non-loopback address is refused, and
a busy port just prints a warning to stderr and leaves the TUI working.

`VPSTUI_LOG_TAIL` sets how many lines `docker logs --tail` replays when a log
screen opens — default 2000, clamped to 1..100000, ignored if unparseable. The
view then keeps up to 8 MiB (or 100k lines) of history, dropping whole older
lines behind a `… (older lines dropped) …` marker.

## Secret storage

Passwords and key passphrases are stored in the OS keyring
(service `vpstui`) when available, otherwise in
`~/.config/vpstui/secrets.json` (mode 0600, plaintext). Private keys
themselves are never copied — only their path is saved. Password auth over
the network is weaker than keys; prefer ssh-agent or key auth.

## Requirements

Targets must run an SSH server and Docker (`docker` in PATH, user allowed to
access the daemon). No agent or daemon-side component is installed.
