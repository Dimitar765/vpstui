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
- **Container actions** — start (`s`), stop (`x`), restart (`r`) with `y/N`
  confirmation.
- **Log streaming** — follow `docker logs -f` in a scrollable viewport
  (`enter` from the dashboard, `esc` to stop).

## Run

```sh
go build -o vpstui .
./vpstui
```

Keys are shown in the help bar of each screen; `ctrl+c` quits from anywhere.

## Secret storage

Passwords and key passphrases are stored in the OS keyring
(service `vpstui`) when available, otherwise in
`~/.config/vpstui/secrets.json` (mode 0600, plaintext). Private keys
themselves are never copied — only their path is saved. Password auth over
the network is weaker than keys; prefer ssh-agent or key auth.

## Requirements

Targets must run an SSH server and Docker (`docker` in PATH, user allowed to
access the daemon). No agent or daemon-side component is installed.
