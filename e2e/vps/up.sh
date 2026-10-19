#!/usr/bin/env bash
# e2e/vps/up.sh — build + boot the throwaway "VPS" container and install
# vpstui-server into it via scripts/install.sh (README flow, --bin adjustment).
# Loopback-only: publishes 127.0.0.1:2223 -> 22. Rerunning RESETS the VPS to a
# clean install (docker rm -f first); daemon data lives only inside the container.
set -euo pipefail
cd "$(dirname "$0")/../.."

NAME=vpstui-vps
PORT=2223
KEY="$HOME/.ssh/vpstui_vps"

[ -f "$KEY" ] || ssh-keygen -t ed25519 -N '' -C vpstui-vps -f "$KEY" >/dev/null
[ -f internal/server/webout/index.html ] || make web   # embed the real GUI, not the stub
go build -o vpstui-server ./cmd/vpstui-server

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker build -q -t "$NAME" --build-arg SSH_PUBKEY="$(cat "${KEY}.pub")" e2e/vps >/dev/null
docker run -d --name "$NAME" --hostname "$NAME" --privileged --cgroupns=host \
  -p "127.0.0.1:${PORT}:22" "$NAME" >/dev/null

ssh-keygen -R "[127.0.0.1]:${PORT}" >/dev/null 2>&1 || true   # fresh host key per build
echo -n "waiting for sshd in the container… "
until ssh -i "$KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
        -p "$PORT" root@127.0.0.1 true 2>/dev/null; do sleep 1; done
echo up

docker cp vpstui-server "$NAME":/tmp/vpstui-server
docker cp scripts/install.sh "$NAME":/tmp/install.sh
docker exec "$NAME" bash /tmp/install.sh \
  --bin /tmp/vpstui-server \
  --admin-email admin@vpstui.local \
  --admin-password vpstui-dev-pass
