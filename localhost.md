# Connecting to localhost safely

vpstui has one transport: it dials an SSH server and runs `docker`, `awk` and
`ps` **there**, plus one read-only `sh` that opens each container's cgroup files for
the all-time peak (`f` on the perf screen). So "this machine" is only reachable once
something listens for SSH on it — and the moment it is, vpstui's write keys act on the
**real** docker daemon of the machine you are typing on. This file records the current
local setup and the rules for using it. Background and the general recipe:
[README → Managing this machine](README.md#managing-this-machine).

The cgroup probe is the only part of vpstui that reads outside `docker`, and on this
box it works as the unprivileged `echo` account: `/sys/fs/cgroup/system.slice/` is
world-readable, so `M.PEAK` and `CPU S` are real measurements here rather than `-`.

## What is running now

| | |
|---|---|
| Server | `/usr/bin/sshd -f ~/.config/vpstui/sshd-local/sshd_config -D`, started as the `echo` account — **no sudo, no systemd** |
| Socket | `127.0.0.1:2222` and nothing else; the system sshd is untouched and `:22` is closed |
| Login key | `~/.ssh/vpstui_local` (ed25519, **no passphrase**), used only against this loopback port |
| Authorization | one line in `~/.ssh/authorized_keys`: `from="127.0.0.1",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc <key>` |
| Host key | `~/.config/vpstui/sshd-local/host_ed25519` — keep it, or every client's TOFU record breaks |
| Pid / log | `~/.config/vpstui/sshd-local/sshd.pid` / `…/log` |

It dies on reboot. Re-arm with:

```sh
setsid /usr/bin/sshd -f ~/.config/vpstui/sshd-local/sshd_config -D -E ~/.config/vpstui/sshd-local/log &
```

`setsid` is required: a plain backgrounded child of a non-login shell exits after
its first connection. The `setgroups() failed: Operation not permitted` line in
the log is expected and harmless — the session still inherits the `docker` group.

## The host entry

`n` in the host list, then: name `this box`, address `127.0.0.1`, port `2222`,
user `echo`, auth `key`, key path `~/.ssh/vpstui_local`.

vpstui never reads `~/.ssh/config`, so the entry must carry address, port, user
and key explicitly. The host key is trusted on first connect into
`~/.config/vpstui/known_hosts`; a later change blocks the connection, as it
would for any VPS.

## Sanity-check the channel, not the app

```sh
ssh -i ~/.ssh/vpstui_local -o BatchMode=yes -p 2222 echo@127.0.0.1 'echo ok'
```

Must print exactly `ok`. Anything else on that line — a motd, a `.zshenv` echo,
an interactive-only prompt — is prefixed to every parsed `docker` output and
will corrupt the dashboard, not just the shell.

## Rules for this host

- **Write actions are the dangerous part.** `x` (stop), `D` (remove), `p`/`P`
  (prune) and the confirmations behind them hit real development containers
  here. Do not exercise them against `this box` to test a feature; use a scratch
  container built from an image already on disk, and remove that one only.
- **Never write `~/.config/vpstui/hosts.json` or `tags.json`** on the user's
  behalf — they add entries in the TUI. Verification runs against an isolated
  `XDG_CONFIG_HOME=/tmp/…`.
- **Never use keyring-stored passwords** to reach the user's real VPSes. Local
  verification uses the dedicated loopback key only.
- **Do not kill the user's running vpstui** (it owns `127.0.0.1:8712`). Probe
  instances bind a different loopback port via `VPSTUI_HTTP_ADDR`.
- **Keep it loopback.** Never widen `ListenAddress`, never add `Port 22`, never
  drop the `from="127.0.0.1"` restriction, and never reuse a real identity key
  in place of `vpstui_local`.
- The `no-user-rc` in the key options is load-bearing, not decoration: without
  it any `~/.zshenv` output lands inside command results.

## Undo

```sh
kill "$(cat ~/.config/vpstui/sshd-local/sshd.pid)"   # stop the local sshd
rm -rf ~/.config/vpstui/sshd-local                   # its config and host key
rm ~/.ssh/vpstui_local ~/.ssh/vpstui_local.pub       # the login key
# then delete the one matching line from ~/.ssh/authorized_keys
```

And remove the `this box` entry in the TUI. Nothing else on the system was
changed: no package installed, no unit enabled, no file outside `~/.ssh`,
`~/.config/vpstui/sshd-local` and this repository.
