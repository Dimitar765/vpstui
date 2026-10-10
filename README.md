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
  (`enter` from the dashboard, `esc` to stop).

## Run

```sh
go build -o vpstui .
./vpstui
```

Keys are shown in the help bar of each screen; `ctrl+c` quits from anywhere.
Vim-style navigation works throughout: `j`/`k` move rows or scroll, `g`/`G`
jump to top/bottom, and help bars are pinned to the last terminal row.

## Secret storage

Passwords and key passphrases are stored in the OS keyring
(service `vpstui`) when available, otherwise in
`~/.config/vpstui/secrets.json` (mode 0600, plaintext). Private keys
themselves are never copied — only their path is saved. Password auth over
the network is weaker than keys; prefer ssh-agent or key auth.

## Requirements

Targets must run an SSH server and Docker (`docker` in PATH, user allowed to
access the daemon). No agent or daemon-side component is installed.
