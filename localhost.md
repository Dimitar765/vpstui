# Connecting to localhost safely

vpstui has one transport: it dials an SSH server and runs `docker`, `awk` and
`ps` **there**, plus one read-only `sh` that opens each container's cgroup files for
the all-time peak (`f` on the perf screen). So "this machine" is only reachable once
something listens for SSH on it. This file records the current local setup and the
rules for using it. Background and the general recipe:
[README → Managing this machine](README.md#managing-this-machine).

## What is running now

| | |
|---|---|
| SSH server | **`e2e/loopback-sshd`** — a small Go SSH+SFTP server from this repo, run as your own user on `127.0.0.1:2222` only |
| Why not OpenSSH | this box runs SELinux enforcing with unreadable labels on core binaries (`ls -Z /bin/bash` → `?`); a user-started `sshd-session` cannot `execve` any of them (`/bin/bash: Permission denied`), and relabeling needs root. The Go server execs from an ordinary user process, which works |
| Auth | public key only: `~/.ssh/vpstui_local` (ed25519, no passphrase) against `~/.ssh/authorized_keys` (`from="127.0.0.1",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc`) |
| Host key | `~/.config/vpstui/sshd-local/host_ed25519` — keep it, or every client's TOFU record breaks |
| Docker | rootless **podman** via the shim `~/.local/bin/docker` (sets `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`) plus the per-user compose plugin `~/.docker/cli-plugins/docker-compose`. Containers are real; only ports >1024 are bindable |

Start the SSH server with:

```sh
go build -o /tmp/loopback-sshd ./e2e/loopback-sshd
/tmp/loopback-sshd -listen 127.0.0.1:2222 \
  -hostkey ~/.config/vpstui/sshd-local/host_ed25519 \
  -authorized-keys ~/.ssh/authorized_keys
```

It execs commands via `/bin/bash -c` with `~/.local/bin` first on PATH (so the
`docker` shim resolves) and serves the `sftp` subsystem (pkg/sftp server side)
— exactly what vpstui and `vpstui-server` use. PTY sessions are intentionally
not supported: the interactive shell (`e`) is not part of e2e.

Re-arm everything after a reboot:

```sh
systemctl --user enable --now podman.socket   # docker shim depends on this socket
setsid /tmp/loopback-sshd -listen 127.0.0.1:2222 \
  -hostkey ~/.config/vpstui/sshd-local/host_ed25519 \
  -authorized-keys ~/.ssh/authorized_keys &
```

## The host entry

`n` in the host list (or the server form in the GUI/daemon): name `this box`,
address `127.0.0.1`, port `2222`, user `dimitar`, auth `key`, key path
`~/.ssh/vpstui_local`.

vpstui never reads `~/.ssh/config`, so the entry must carry address, port, user
and key explicitly. The Go server does no host-key checking of clients, but
clients still TOFU its host key into their known-hosts.

## Sanity-check the channel, not the app

```sh
ssh -i ~/.ssh/vpstui_local -o BatchMode=yes -p 2222 dimitar@127.0.0.1 'echo ok && docker ps'
```

Must print `ok` and an empty container list. Anything else on that line — a
motd, a shell rc echo — would be prefixed to every parsed `docker` output and
corrupt the dashboard. (`no-user-rc` in the authorized_keys line is load-
bearing; the loopback-sshd additionally runs commands through `bash -c` with a
fixed environment, so rc files do not run at all.)

## Rules for this host

- **Write actions are the dangerous part.** `x` (stop), `D` (remove), `p`/`P`
  (prune) and the confirmations behind them hit real containers on this
  machine. Do not exercise them against `this box` to test a feature; use a
  scratch container built from an image already on disk, and remove that one only.
- **Never write `~/.config/vpstui/hosts.json` or `tags.json`** on the user's
  behalf — they add entries in the TUI. Verification runs against an isolated
  `XDG_CONFIG_HOME=/tmp/…` (`./e2e-drive.sh` and `./e2e-run.sh` do this).
- **Never use keyring-stored passwords** to reach the user's real VPSes. Local
  verification uses the dedicated loopback key only.
- **Do not kill the user's running vpstui** (it owns `127.0.0.1:8712`). Probe
  instances bind a different loopback port via `VPSTUI_HTTP_ADDR`.
- **Keep it loopback.** Never widen the listen address, never add a port, never
  reuse a real identity key in place of `vpstui_local`.

## Undo

```sh
pkill -f loopback-sshd                    # stop the Go SSH server
rm -rf ~/.config/vpstui/sshd-local        # its host key
rm ~/.ssh/vpstui_local ~/.ssh/vpstui_local.pub
# then delete the matching line from ~/.ssh/authorized_keys
rm ~/.local/bin/docker ~/.docker/cli-plugins/docker-compose   # the shims
systemctl --user disable --now podman.socket
```

And remove the `this box` entry in the TUI. Nothing else on the system was
changed: no package installed, no unit enabled beyond the user-level podman
socket, no file outside `~/.ssh`, `~/.config/vpstui/sshd-local`, `~/.local`,
`~/.docker` and this repository.
