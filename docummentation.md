# vpstui — documentation

vpstui is a terminal UI (Go + Bubble Tea) for managing Docker containers on any
number of remote VPS machines over SSH. It has one transport: it dials an SSH
server and runs `docker`, `awk` and `ps` **there** — no agent, no daemon-side
component, nothing installed on the target. The machine vpstui itself runs on is
just another target once something listens for SSH on loopback.

This document is the full reference: setup, every screen and key, the embedded
web API, configuration, file layout, security model, architecture, and testing.
The README carries the marketing summary; this file carries the details.

---

## Table of contents

1. [Requirements](#requirements)
2. [Build and run](#build-and-run)
3. [Host manager](#host-manager)
4. [Authentication and security](#authentication-and-security)
5. [Sessions and global keys](#sessions-and-global-keys)
6. [Screens and keybindings](#screens-and-keybindings)
   - [Dashboard](#dashboard)
   - [Performance ranking](#performance-ranking)
   - [Disk panel](#disk-panel)
   - [Image manager](#image-manager)
   - [Error sweep](#error-sweep)
   - [Logs](#logs)
   - [Interactive shell](#interactive-shell)
7. [Tags](#tags)
8. [Embedded web API](#embedded-web-api)
9. [Configuration reference](#configuration-reference)
10. [Files and directories](#files-and-directories)
11. [Architecture](#architecture)
12. [Testing](#testing)
13. [Troubleshooting](#troubleshooting)

---

## Requirements

- Go (any recent toolchain; the module targets current Go) to build.
- Each **target** host: an SSH server, Docker (`docker` in `PATH`, your SSH user
  allowed to talk to the daemon). Nothing is installed remotely.
- The **error sweep** additionally uses `awk` and `ps` on the target (standard on
  any Docker host). If one is missing the error screen shows that host's shell
  error instead of silently reporting zero errors.
- The **all-time peak probe** reads each container's cgroup files
  (`memory.peak` + `cpu.stat` on cgroup v2, `memory.max_usage_in_bytes` +
  `cpuacct.usage` on v1) as your SSH user. Where none can be read, the ranking
  window columns still work and the all-time columns show `-` with a note.
- Secrets: an OS keyring when available (see
  [Secrets](#authentication-and-security)).

## Build and run

```sh
go build -o vpstui .
./vpstui
```

`ctrl+c` quits from anywhere. Every screen pins a help bar to the last terminal
row listing its keys. Vim-style navigation works throughout: `j`/`k` move rows
or scroll, `g`/`G` jump to top/bottom.

## Host manager

The first screen is the host list. Keys:

| Key | Action |
| --- | --- |
| `↑/↓` `j/k` | move |
| `n` | new host |
| `e` | edit host under cursor |
| `d` | delete host |
| `enter` | connect (or resume a disconnected session) |
| `a` | fleet-wide error screen |
| `q` | quit |

The host form walks fields with `up/down`, `enter` moves to the next field and
saves from the last one, `esc` cancels. A host entry carries: address, port,
user, auth type (`password`, `key`, `agent`) and — for key auth — the private
key path (with `~` and `$VAR` expansion). Passwords and passphrases are asked in
a follow-up prompt and stored separately (see below).

Hosts persist to `~/.config/vpstui/hosts.json`. If a config file cannot be
parsed it is renamed aside to `<file>.bad` on load and never overwritten, so a
bad edit can't destroy your data; the next save writes a fresh file.

## Authentication and security

- **Auth methods**: password, private key (optional passphrase), or ssh-agent
  (`SSH_AUTH_SOCK`).
  - Password auth tries `password` first and then the same secret through
    keyboard-interactive, so PAM hosts that reject plain password auth still
    work.
  - Key auth reads the private key from the configured path; the key itself is
    never copied anywhere — only the path is stored.
- **Trust on first use (TOFU)**: the first connection pins the server's host key
  into `~/.config/vpstui/known_hosts`. A later connection presenting a different
  key is blocked with a "host key … has CHANGED" error naming the file to fix.
  vpstui has its own SSH client (x/crypto/ssh) — `~/.ssh/config` is never read,
  so aliases, `ProxyJump` and `IdentityFile` do not apply; everything is in the
  host entry.
- **Secret storage**: passwords and key passphrases live in the OS keyring
  (service `vpstui`) when a keyring is usable; otherwise they fall back to
  `~/.config/vpstui/secrets.json` (mode 0600, plaintext). Writes go through an
  atomic write helper (temp file, fsync, rename) so a crash can never leave a
  half-written document.
- **Reconnect**: on a dropped connection vpstui retries automatically once,
  ~5 s later; if that fails the dashboard shows
  "connection lost — press c to reconnect" and `c` redials by hand.

**Managing this machine**: vpstui can manage its own host over loopback SSH.
Give it a dedicated, passphrase-less keypair restricted to loopback:

```sh
ssh-keygen -t ed25519 -N '' -C "vpstui@$(hostname)" -f ~/.ssh/vpstui_local
printf 'from="127.0.0.1",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc ' \
  >> ~/.ssh/authorized_keys
cat ~/.ssh/vpstui_local.pub >> ~/.ssh/authorized_keys
```

A dedicated key means the whole feature is removed by deleting two files and one
`authorized_keys` line.

## Sessions and global keys

Each connected host is a session. The tab bar at the top shows every session,
its live status, and which one is active.

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | next / previous session |
| `1`–`9` | jump to session N |
| `X` | close the active session |
| `ctrl+c` | quit |

These keys are inert while the log filter input owns typing, inside the tag
prompt, and on the error screen (it aggregates every host, so switching makes no
sense there). Only the active session's dashboard polls at the 2 s cadence;
background sessions drop to a slower stats-only sample so their ranking and
sparklines stay warm without stealing SSH bandwidth. If the SSH connection dies,
vpstui redials once automatically (~5 s); otherwise `c` on the dashboard
reconnects.

## Screens and keybindings

### Dashboard

The main screen: host metrics (load, memory, CPUs, uptime) and the container
table (name, image, state, status, CPU %, MEM %, MEM USAGE). All of it arrives
in one batched exec (`docker ps` + `docker stats --no-stream` + /proc reads)
refreshed every 2 s; columns adapt to terminal width; states are colour-coded
and high CPU/MEM highlight as they climb.

A `docker events` stream runs alongside: container lifecycle events (start,
stop, die, …) trigger an immediate re-fetch instead of waiting for the next
tick, so a `docker stop` from another shell shows up within about one snapshot.
If an event lands while a fetch is in flight it coalesces into the next refresh
rather than stacking round trips.

| Key | Action |
| --- | --- |
| `↑/↓` `j/k`, `g/G` | navigate |
| `enter` | open logs for the container under the cursor |
| `d` | container detail (`docker inspect`) |
| `space` | fold/unfold the compose project group |
| `s` / `x` / `r` | start / stop / restart (with `y/N` confirmation) |
| `D` | remove container — stopped ones only (`y/N`) |
| `e` | interactive shell in the container |
| `i` | disk panel |
| `I` | image manager |
| `p` | performance ranking |
| `<` / `>` | cycle row order: grouped → name → state → cpu% → mem% (and back) |
| `t` / `T` | tag / untag the container |
| `a` | fleet-wide error screen |
| `f` | refresh now (re-arms the error sweep and peak probe too) |
| `c` | reconnect (only while disconnected) |
| `esc` | back to the host list |

Row order defaults to compose-project grouping (containers without a project
sort first, fold with `space`). The flat sorts (`<`/`>`) drop the group headers;
`state` ranks running → paused → restarting → created → exited → dead, and the
cpu%/mem% sorts are descending with never-sampled containers last. Every sort
breaks ties by name. The choice resets when the session reconnects.

Action results appear as a toast line under the header and clear on the next
tick. Prune actions live in the disk panel (`p`/`P` there, not on the
dashboard).

### Performance ranking

`p` opens a per-container CPU ranking that re-samples every 10 s (independent of
the 2 s dashboard tick, so it stays warm even for background hosts) and re-ranks
rows live. The cursor is anchored to the container, not the row number, so a
re-rank doesn't teleport it. Columns cover the recent window plus **all-time
peaks** from the kernel's cgroup counters, probed once per connection (and again
on `f`).

| Key | Action |
| --- | --- |
| `j/k`, `g/G` | navigate |
| `enter` | open that container's logs |
| `t` / `T` | tag / untag |
| `i` | disk panel |
| `f` | re-probe all-time peaks |
| `esc` | close |

### Disk panel

`i` runs `docker system df`.

| Key | Action |
| --- | --- |
| `p` | prune dangling images (`y/N`) |
| `P` | prune the whole system (`y/N`) |
| `f` | refresh |
| `esc` | close |

### Image manager

`I` lists the host's images (`docker images`) in a table: repository, tag,
created, size, short ID. Removal confirms with `y/N` and shows the exact
`repository:tag` in the prompt; the list refetches itself after every action
(removal, prune, container actions) while the panel is open.

| Key | Action |
| --- | --- |
| `j/k`, `g/G` | navigate |
| `D` | remove the image under the cursor (`y/N`; fails harmlessly if it is in use) |
| `f` | refresh |
| `esc` | close |

### Error sweep

`a` (from the host list, dashboard or perf screen) aggregates recent errors from
**every** host in one batched sweep: each container's recent logs are fetched in
a single SSH round trip per host, grouped into signatures. Where the sweep found
nothing there is coverage info, so "no errors" never means "didn't look".

| Key | Action |
| --- | --- |
| `j/k`, `g/G` | navigate |
| `enter` | expand the group under the cursor |
| `l` | toggle live mode (keep sweeping) |
| `y` / `Y` | yank the line / the whole signature to the clipboard |
| `m` / `w` | cycle grouping mode |
| `r` | sweep again now |
| `q` / `esc` | back |

### Logs

`enter` on a container (dashboard, perf screen, or error screen) opens its log
view. The stream replays the tail (`VPSTUI_LOG_TAIL`, default 2000 lines) and
then follows. The buffer keeps up to 8 MiB or 100k lines, dropping whole older
lines behind a `… (older lines dropped) …` marker rather than splitting one.
While lines stream in, `↓ N new` counts what's waiting below; jumping to the
bottom consumes it.

| Key | Action |
| --- | --- |
| `j/k`, `g/G` | scroll (following the bottom when you land there) |
| `/` | start a filter query |
| `m` | switch the query mode: highlight ↔ hide matching lines |
| `n` / `N` | next / previous match |
| `E` | export the whole buffered history to a file |
| `esc` | step out: typing → committed filter → no filter → back to dashboard |

The filter is live while typing; `enter` commits it. While the input owns keys,
typing is the query — session-switching keys are inert until you leave the
field.

**`E` export** writes everything currently buffered (oldest first, newline
terminated) to
`~/.local/share/vpstui/logs/<host>-<container>-<timestamp>.log`
(`XDG_DATA_HOME` respected), mode 0600, atomically. The header shows
`exporting…`, then `saved: <path>` (or the failure). An empty buffer is a no-op
(`nothing to export`); a second `E` while one is in flight is ignored.

### Interactive shell

`e` on a running container suspends the TUI and attaches a real PTY shell
inside it (`docker exec -i -t`, bash with `/bin/sh` fallback) over the same SSH
connection; `exit` returns to the dashboard and the table refreshes. A probe
checks the shell exists first; shells only start in running containers.

## Tags

`t` labels the container under the cursor, `T` removes the label. Inside the
prompt `tab` cycles the colour palette, `enter` commits, `esc` discards. Tags
are shown ahead of the docker name in the dashboard, perf ranking, error screen
and web page (read-only there), and persist per host in
`~/.config/vpstui/tags.json`. An unknown colour in a hand-edited file falls back
to untagged rather than crashing.

## Embedded web API

While the TUI runs it serves a read-only JSON API on loopback — port **8712**
by default (`VPSTUI_HTTP_ADDR` to change; non-loopback bind addresses are
refused, a busy port prints a warning and leaves the TUI working). Handlers read
an in-memory snapshot republished on the 2 s tick, so the API costs no extra SSH
traffic and dies with the TUI.

| Endpoint | Returns |
| --- | --- |
| `GET /` | bundled single-file dashboard page (polls the API, default 5 s) |
| `GET /healthz` | `{"ok":true}` |
| `GET /api/sessions` | session list (id, name, target, connected, fetchedAt, containerCount) |
| `GET /api/sessions/{id}` | host stats, container rows, perf ranking |
| `GET /api/sessions/{id}/network` | container ↔ network graph |
| `GET /api/errors` | cross-host error groups and sweep coverage |

Unknown ids → `404`; non-GET → `405`; after the TUI starts quitting → `503`.
`cpu`/`memPerc`/`memUsage` are docker's pre-formatted strings (`"-"` before the
first sample), `cpuPct`/`memPct` the parsed numbers or `null`. Tags are
read-only in the browser and rendered in their palette colour.

## Configuration reference

| Variable | Default | Meaning |
| --- | --- | --- |
| `VPSTUI_HTTP_ADDR` | `127.0.0.1:8712` | web API bind address; non-loopback refused |
| `VPSTUI_LOG_TAIL` | `2000` (clamp 1..100000) | lines replayed when a log screen opens; unparseable → default |
| `VPSTUI_ERR_SINCE` | `2h` (clamp 7 days; `d`/`w` understood) | how far back the error sweep looks |
| `VPSTUI_ERR_TAIL` | `500` (clamp 1..5000) | per-container `--tail` for the sweep |
| `XDG_CONFIG_HOME` | `~/.config` | where `vpstui/` config lives |
| `XDG_DATA_HOME` | `~/.local/share` | where `vpstui/logs/` exports live |

## Files and directories

| Path | Contents |
| --- | --- |
| `~/.config/vpstui/hosts.json` | host entries (address, port, user, auth type, key path) |
| `~/.config/vpstui/known_hosts` | TOFU-pinned host keys; a changed key blocks the dial |
| `~/.config/vpstui/secrets.json` | password/passphrase fallback (0600) when no keyring |
| `~/.config/vpstui/tags.json` | container labels + palette colours |
| `~/.config/vpstui/*.bad` | unparseable files renamed aside by the loader |
| `~/.local/share/vpstui/logs/` | `E`-exported log files (`<host>-<container>-<ts>.log`, 0600) |

All config writes are atomic (temp file → fsync → rename, parent dir fsynced).

## Architecture

```
main.go                 program wiring: model, sender, HTTP server
internal/app/           Bubble Tea model, all screens and key handling
  model.go              root model: sessions, message routing, tags of freshness
  dashboard.go          container table, actions, sort, events coalescing, images
  container.go          log view: buffer, filter, export
  perfview.go           10 s ranking with all-time peaks
  errview.go/errors.go  fleet error sweep: fetch, grouping, yank
  ctable.go             minimal fixed-column table with per-column styling
  hostlist.go/hostform.go host manager
  tags.go               label palette, prompt, persistence glue
internal/sshclient/     the SSH client itself: Dial (TOFU), Exec, StreamExec,
                        keepalive with probe budget, typed ErrConnLost
internal/docker/        remote command layer: one batched snapshot exec, NDJSON
                        parsers, images/events, quoting and friendly errors
internal/perf/          CPU/mem history ring buffer, percent parsing
internal/config/        XDG paths, hosts/tags persistence, atomic writes
internal/secret/        keyring with plaintext-file fallback
internal/httpapi/       loopback JSON API + bundled single-file web page
internal/shell/         PTY hand-off for `docker exec -i -t`
internal/errsample/     error line sampling/grouping rules
internal/models/        shared data types (Host, Container, Image, …)
```

Design invariants worth knowing when changing code:

- Every remote command is quoted through one helper; image/container IDs are
  validated against a hex pattern before they reach a shell.
- UI state updates happen only on the Bubble Tea goroutine; background work
  returns messages. Each message carries a `msgTag` (host + connection
  generation) and stale messages are dropped, so a reply from a dead connection
  can never mutate the new one. Every (re)connect mints a fresh Dashboard.
- The refresh chain is guarded: at most one snapshot in flight per session;
  events that arrive mid-fetch set a pending flag flushed by the next completed
  fetch. The 2 s tick remains the floor, events are the accelerator.
- Error wrapping: transport failures surface as `sshclient.ErrConnLost`
  (`errors.Is`-detectable); docker daemon problems become human sentences.

## Testing

```sh
go test ./...           # unit tests for every package
go test -race ./...     # the concurrency-sensitive paths under the race detector
go test ./internal/sshclient/ -v   # integration tests
```

The sshclient package ships integration tests against an **in-process SSH
server** built only from `x/crypto/ssh` (no external sshd, no new dependencies):
Dial with password / key / keyboard-interactive-only auth, wrong password,
Exec output and exit status, Exec timeout, connection loss mid-exec mapped to
`ErrConnLost`, stream lifecycle, keepalive probe budget (one slow probe must not
kill the connection; consecutive failures must), and both TOFU paths (first-use
pinning, changed-key blocking, pin-write failure).

App-layer tests cover the same behaviours at message level: sort cycling, image
panel, event arming/coalescing/stale-drop, log export, retries. Dashboards in
tests wrap a connection-less executor, so commands are handed out but never
dialed.

## Troubleshooting

| Symptom | Meaning / fix |
| --- | --- |
| `docker is not installed or not in PATH on this host` | the target has no `docker` for your SSH user |
| `docker daemon is not running on this host` | start the daemon on the target |
| `permission denied accessing /var/run/docker.sock — add the user to the docker group` | SSH user lacks daemon access |
| `command timed out: …` | the batched snapshot exceeded its window; check target load |
| `host key for … has CHANGED` | server key changed; verify out of band, then fix/delete the line in `known_hosts` |
| `first-use host key pin failed` | `known_hosts` could not be written (permissions); the connection is refused, nothing trusted |
| `no password provided` / `no private key path configured` | host entry incomplete — edit it |
| `SSH_AUTH_SOCK is not set; no ssh-agent available` | agent auth selected but no agent running |
| `private key … is passphrase-protected; provide the passphrase` | key needs its secret stored (or use a passphrase-less key) |
| `host: sh: awk: command not found` on the error screen | sweep needs `awk`/`ps` on that target |
| Web API dead | it only lives while the TUI runs; check the `VPSTUI_HTTP_ADDR` warning on stderr |
| Stream ended right after opening logs | connection dropped — the typed banner / auto-retry applies |
