#!/bin/sh
# e2e-drive.sh — scripted, headless TUI proof on top of e2e-run.sh.
# Drives the real binary under tmux with the throwaway e2e config and the
# loopback sshd target (127.0.0.1:2222), asserting on-screen text per step.
# Never touches ~/.config/vpstui. Run from the repo root: ./e2e-drive.sh
set -eu
cd "$(dirname "$0")"

CFG=/tmp/vpstui-e2e/cfg/vpstui
SESS=vpstui-e2e-drive
[ -d "$CFG" ] || { echo "run: mkdir -p $CFG first (e2e config missing)" >&2; exit 1; }
[ -x ./vpstui ] || { echo "build ./vpstui first" >&2; exit 1; }
command -v tmux >/dev/null || { echo "tmux required" >&2; exit 1; }

# The loopback target row. Throwaway: key auth against the loopback sshd only.
cat > "$CFG/hosts.json" <<EOF
[{"id":"e2e-loop","name":"loop","address":"127.0.0.1","port":2222,"user":"$(whoami)","authType":"key","keyPath":"$HOME/.ssh/vpstui_local"}]
EOF

tmux kill-session -t "$SESS" 2>/dev/null || true
tmux new-session -d -x 120 -y 36 -s "$SESS" "./e2e-run.sh"
sleep 2 # let the TUI take over the pane before any keys are sent

grab() { tmux capture-pane -t "$SESS" -p; }
type() { tmux send-keys -t "$SESS" -l "$1"; sleep 0.4; }
key()  { tmux send-keys -t "$SESS" "$1"; sleep 0.6; }
expect() { # expect <text> [tries]
  n=${2:-10}
  i=0
  while [ $i -lt $n ]; do
    if grab | grep -q -- "$1"; then echo "ok: $1"; return 0; fi
    sleep 0.5
    i=$((i + 1))
  done
  echo "FAIL: expected on screen: $1" >&2
  grab | sed -n '1,32p' >&2
  tmux kill-session -t "$SESS" 2>/dev/null || true
  exit 1
}
absent() {
  if grab | grep -q -- "$1"; then
    echo "FAIL: still on screen: $1" >&2
    tmux kill-session -t "$SESS" 2>/dev/null || true
    exit 1
  fi
}

# --- connect the loopback host; wait for the dashboard ---
key Enter
expect "load" 20

# --- 1. help overlay opens with ? and any key closes it ---
key "?"
expect "keys — dashboard"
key Escape
if grab | grep -q "keys — dashboard"; then
  echo "FAIL: overlay did not close" >&2; exit 1
fi
echo "ok: overlay closed"

# --- 2. command palette: :logs <name> jumps to the log view ---
key ":"
type "logs e2e-nginx"
key Enter
expect "logs — e2e-nginx" 15
key Escape
expect "load" 5

# --- 3. dashboard filter, then esc restores ---
key "/"
type "nginx"
key Enter
expect "filter: nginx"
key Escape
if grab | grep -q "filter: nginx"; then
  echo "FAIL: esc did not clear the filter" >&2; exit 1
fi
echo "ok: esc cleared the filter"

# --- 4. multi-select: v marks two rows, x names both in ONE confirmation ---
key v
key Down
key v
key x
expect "stop 2 marked containers"
key n # cancel — no docker action fires

# --- 5. log view wrap toggle ---
key "/" # filter to the fixture row regardless of dashboard ordering
type "nginx"
key Enter
expect "filter: nginx" 5
key V # clear any marks left by step 4
key g # cursor to the top of the FILTERED list = e2e-nginx
key Enter
expect "logs — e2e-nginx" 15
key w
# The wrap hint lives in the ? overlay (bar shows the top 5 keys), so assert
# on the persisted setting instead — proof the toggle fired and saved.
i=0
while ! grep -q '"logWrap": true' "$CFG/settings.json" 2>/dev/null; do
  [ $i -lt 10 ] || { echo "FAIL: w did not persist logWrap" >&2; exit 1; }
  sleep 0.5
  i=$((i + 1))
done
echo "ok: logWrap persisted"

tmux kill-session -t "$SESS"
echo "e2e-drive: all steps passed"
