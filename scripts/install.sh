#!/usr/bin/env bash
# vpstui-server installer: turns a systemd Linux VPS into a running
# vpstui-server daemon with an admin account and — by default — everything
# prepared to host deployments (docker, sshd, a loopback key and a
# self-registered first managed server). The daemon binds 127.0.0.1 only;
# reach it through the SSH tunnel printed at the end.
#
# Quick start:
#   curl -fsSL https://raw.githubusercontent.com/Dimitar765/vpstui/main/scripts/install.sh | bash
#
# Idempotent: re-running with the same arguments upgrades the binary and the
# unit file, and leaves data, admin account and registered servers untouched.
#
# Only hard dependencies: curl, tar, sha256sum (present on every systemd
# distro). JSON is parsed with grep/sed; no jq or python required.

set -euo pipefail

REPO="Dimitar765/vpstui"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"
LATEST_API="https://api.github.com/repos/${REPO}/releases/latest"

VERSION=""                       # empty = resolve latest
WITH_DOCKER=1
ADMIN_EMAIL="admin@vpstui.local"
ADMIN_PASSWORD=""                # empty = generated
ACME_EMAIL=""
PORT=8713
BIN_DIR="/usr/local/bin"
DATA_DIR="/var/lib/vpstui-server"

ARCH=""
TMPDIR_DL=""
API_BODY_FILE=""
API_STATUS=""
API_BODY=""
BEARER=""                        # admin session token
API_KEY=""                       # vps_... key, printed once
CONFIGURED=0                     # 1 = install was already set up (upgrade)
REGISTERED=""                    # "" skipped, "yes", "failed", "already"

log()  { printf '[vpstui-install] %s\n' "$*"; }
warn() { printf '[vpstui-install] WARNING: %s\n' "$*" >&2; }
die()  { printf '[vpstui-install] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: install.sh [options]

  --version <tag>        release tag to install (default: latest release)
  --no-docker            skip VPS prep entirely (no docker, no ssh key, no
                         self-registration); daemon only
  --admin-email <addr>   admin email (default: ${ADMIN_EMAIL})
  --admin-password <pw>  admin password, min 8 chars (default: generated)
  --acme-email <addr>    ACME account email for TLS, '' to disable
                         (default: disabled)
  --port <port>          local listen port; the daemon always binds
                         127.0.0.1 (default: ${PORT})
  --bin-dir <dir>        binary location (default: ${BIN_DIR})
  --data-dir <dir>       daemon data directory (default: ${DATA_DIR})
  -h | --help            this help
EOF
}

# ---------------------------------------------------------------- arguments

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        [[ $# -ge 2 ]] || die "--version requires a tag argument"
        VERSION=$2; shift 2 ;;
      --no-docker)
        WITH_DOCKER=0; shift ;;
      --admin-email)
        [[ $# -ge 2 ]] || die "--admin-email requires a value"
        ADMIN_EMAIL=$2; shift 2 ;;
      --admin-password)
        [[ $# -ge 2 ]] || die "--admin-password requires a value"
        ADMIN_PASSWORD=$2; shift 2 ;;
      --acme-email)
        [[ $# -ge 2 ]] || die "--acme-email requires a value (pass '' for none)"
        ACME_EMAIL=$2; shift 2 ;;
      --port)
        [[ $# -ge 2 ]] || die "--port requires a value"
        PORT=$2; shift 2 ;;
      --bin-dir)
        [[ $# -ge 2 ]] || die "--bin-dir requires a value"
        BIN_DIR=$2; shift 2 ;;
      --data-dir)
        [[ $# -ge 2 ]] || die "--data-dir requires a value"
        DATA_DIR=$2; shift 2 ;;
      -h|--help)
        usage; exit 0 ;;
      *)
        usage >&2; die "unknown argument: $1" ;;
    esac
  done

  [[ $VERSION == latest ]] && VERSION=""
  [[ $PORT =~ ^[0-9]+$ ]] || die "--port must be a number, got: ${PORT}"
  (( PORT >= 1 && PORT <= 65535 )) || die "--port out of range: ${PORT}"
  [[ $BIN_DIR == /* ]] || die "--bin-dir must be an absolute path"
  [[ $DATA_DIR == /* ]] || die "--data-dir must be an absolute path"
  [[ $ADMIN_EMAIL == *@* ]] || die "--admin-email must contain '@'"
  (( ${#ADMIN_PASSWORD} == 0 || ${#ADMIN_PASSWORD} >= 8 )) \
    || die "--admin-password must be at least 8 characters"

  local v
  for v in "$ADMIN_EMAIL" "$ADMIN_PASSWORD" "$ACME_EMAIL"; do
    case "$v" in
      *[\"\\]*) die "email/password values may not contain quotes or backslashes" ;;
    esac
  done

  if [[ -z $ADMIN_PASSWORD ]]; then
    ADMIN_PASSWORD=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
  fi
}

# ---------------------------------------------------------------- preflight

port_listening() {   # true if anything listens on :$PORT
  if command -v ss >/dev/null 2>&1; then
    ss -lHtn "sport = :${PORT}" 2>/dev/null | grep -q .
  else
    (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null
  fi
}

preflight() {
  [[ $(id -u) -eq 0 ]] || die "must run as root (try: sudo -i, then re-run this script)"

  case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) die "unsupported architecture: $(uname -m) (need x86_64 or aarch64)" ;;
  esac

  command -v systemctl >/dev/null 2>&1 || die "systemd is required (systemctl not found)"

  local missing=() t
  for t in curl tar sha256sum; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  if (( ${#missing[@]} > 0 )); then
    die "missing tools: ${missing[*]} — install with: apt-get install -y ${missing[*]}  (or: dnf install -y ${missing[*]})"
  fi

  if port_listening; then
    if systemctl is-active --quiet vpstui-server; then
      log "vpstui-server is running on port ${PORT} — upgrade run"
    else
      die "port ${PORT} is already in use by another process and vpstui-server is not active"
    fi
  fi
}

# ------------------------------------------------------- download + verify

download_release() {
  if [[ -z $VERSION ]]; then
    log "resolving latest release"
    VERSION=$(curl -fsSL "$LATEST_API" | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/.*: *"//;s/"$//') \
      || die "could not resolve the latest release tag from ${LATEST_API}"
    [[ -n $VERSION ]] || die "could not resolve the latest release tag from ${LATEST_API}"
    log "latest release: ${VERSION}"
  fi

  TMPDIR_DL=$(mktemp -d)
  API_BODY_FILE="${TMPDIR_DL}/api-body.json"
  trap 'rm -rf "$TMPDIR_DL"' EXIT

  local base="${DOWNLOAD_BASE}/${VERSION}" asset="vpstui-server_linux_${ARCH}.tar.gz"
  log "downloading vpstui-server ${VERSION} for linux/${ARCH}"
  curl -fsSL -o "${TMPDIR_DL}/checksums.txt" "${base}/checksums.txt" \
    || die "download failed: checksums.txt (does release ${VERSION} exist?)"
  curl -fsSL -o "${TMPDIR_DL}/asset.tar.gz" "${base}/${asset}" \
    || die "download failed: ${asset} (does release ${VERSION} include linux/${ARCH}?)"

  # checksums.txt lists "../<asset>" (sha256sum ran inside dist/); strip the
  # prefix so -c resolves against this directory. A missing line makes
  # sha256sum fail closed.
  ( cd "$TMPDIR_DL" \
      && grep -F "${asset}" checksums.txt | sed -e 's#\.\./##' | sha256sum -c - ) \
    || die "checksum verification failed for ${asset}"
  tar -xzf "${TMPDIR_DL}/asset.tar.gz" -C "$TMPDIR_DL"
  [[ -f "${TMPDIR_DL}/vpstui-server" ]] || die "archive did not contain the vpstui-server binary"
}

# ------------------------------------------------------------------ install

install_binary() {
  if systemctl is-active --quiet vpstui-server; then
    log "stopping running vpstui-server to replace the binary"
    systemctl stop vpstui-server
  fi

  if ! id vpstui >/dev/null 2>&1; then
    getent group vpstui >/dev/null 2>&1 || groupadd --system vpstui
    useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin -g vpstui vpstui
  fi

  install -d -m 0755 "$BIN_DIR"
  install -m 0750 -o root -g vpstui "${TMPDIR_DL}/vpstui-server" "${BIN_DIR}/vpstui-server"

  mkdir -p "$DATA_DIR"
  chmod 0700 "$DATA_DIR"
  chown vpstui:vpstui "$DATA_DIR"
  log "binary installed: ${BIN_DIR}/vpstui-server"
}

write_unit() {
  local exec_start="${BIN_DIR}/vpstui-server --listen 127.0.0.1:${PORT} --data ${DATA_DIR}"
  if [[ -n $ACME_EMAIL ]]; then
    exec_start+=" --acme-email '${ACME_EMAIL}'"
  fi

  local tmp
  tmp=$(mktemp)
  cat >"$tmp" <<EOF
[Unit]
Description=vpstui-server PaaS daemon
After=network-online.target
Wants=network-online.target

[Service]
User=vpstui
Group=vpstui
ExecStart=${exec_start}
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
  mv "$tmp" /etc/systemd/system/vpstui-server.service
  log "unit written: /etc/systemd/system/vpstui-server.service"
}

activate() {
  systemctl daemon-reload
  systemctl enable --now vpstui-server
  log "vpstui-server enabled and started"
}

wait_health() {
  local _
  for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
      log "daemon healthy (http://127.0.0.1:${PORT}/healthz)"
      return 0
    fi
    sleep 1
  done
  journalctl -u vpstui-server --no-pager -n 50 2>/dev/null || true
  die "vpstui-server did not become healthy within 30s (journal excerpt above)"
}

# ------------------------------------------------------------ API helpers

# api_post ENDPOINT JSON_PAYLOAD [BEARER] — sets API_STATUS and API_BODY.
api_post() {
  local ep=$1 payload=$2 bearer=${3:-}
  local -a hdr=()
  if [[ -n $bearer ]]; then hdr+=(-H "Authorization: Bearer ${bearer}"); fi
  if ! API_STATUS=$(curl -sS -X POST -H 'Content-Type: application/json' \
      "${hdr[@]}" -d "$payload" -o "$API_BODY_FILE" -w '%{http_code}' \
      "http://127.0.0.1:${PORT}${ep}"); then
    die "cannot reach the vpstui-server API at ${ep}"
  fi
  API_BODY=$(cat "$API_BODY_FILE")
}

# api_get ENDPOINT [BEARER] — sets API_STATUS and API_BODY.
api_get() {
  local ep=$1 bearer=${2:-}
  local -a hdr=()
  if [[ -n $bearer ]]; then hdr+=(-H "Authorization: Bearer ${bearer}"); fi
  if ! API_STATUS=$(curl -sS "${hdr[@]}" -o "$API_BODY_FILE" -w '%{http_code}' \
      "http://127.0.0.1:${PORT}${ep}"); then
    die "cannot reach the vpstui-server API at ${ep}"
  fi
  API_BODY=$(cat "$API_BODY_FILE")
}

# json_field NAME — first "NAME":"value" in API_BODY, value on stdout.
json_field() {
  printf '%s' "$API_BODY" \
    | grep -o "\"${1}\": *\"[^\"]*\"" | head -n1 | sed 's/^[^:]*: *"//;s/"$//'
}

# ---------------------------------------------------------- admin bootstrap

bootstrap_admin() {
  if [[ ! -s "${DATA_DIR}/setup-token" ]]; then
    # The daemon writes a setup token at boot only while no admin exists, so
    # a healthy daemon without one means this installation is already set up.
    log "no setup token and daemon healthy — already configured, skipping admin bootstrap"
    CONFIGURED=1
    return 0
  fi

  local token
  token=$(tr -d '[:space:]' <"${DATA_DIR}/setup-token")
  [[ -n $token ]] || die "setup token file is empty; restart vpstui-server and re-run"

  log "creating admin account (${ADMIN_EMAIL})"
  api_post /api/setup "{\"token\":\"${token}\",\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASSWORD}\"}"
  [[ $API_STATUS == 200 ]] || die "admin setup failed (HTTP ${API_STATUS}): ${API_BODY}"

  api_post /api/auth/login "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASSWORD}\"}"
  [[ $API_STATUS == 200 ]] || die "login failed after setup (HTTP ${API_STATUS}): ${API_BODY}"
  BEARER=$(json_field token)
  [[ -n $BEARER ]] || die "login response did not contain a token: ${API_BODY}"

  api_post /api/keys '{"name":"install"}' "$BEARER"
  [[ $API_STATUS == 201 ]] || die "API key creation failed (HTTP ${API_STATUS}): ${API_BODY}"
  API_KEY=$(json_field token)
  [[ -n $API_KEY ]] || die "key response did not contain a token: ${API_BODY}"
  log "admin account and install API key created"
}

# ---------------------------------------------------------------- VPS prep

prep_docker() {
  if command -v docker >/dev/null 2>&1; then
    log "docker already installed: $(docker --version 2>/dev/null || echo docker)"
  else
    log "installing docker (official get.docker.com script)"
    curl -fsSL https://get.docker.com | sh \
      || die "docker install failed — install docker manually and re-run this script"
  fi
  if ! systemctl enable --now docker >/dev/null 2>&1; then
    systemctl start docker >/dev/null 2>&1 || die "could not start the docker service"
  fi
  if ! docker compose version >/dev/null 2>&1; then
    die "docker compose plugin missing: 'docker compose version' failed"
  fi
  log "docker ready with compose: $(docker compose version | head -n1)"
}

port22_listening() {
  if command -v ss >/dev/null 2>&1; then
    ss -lHtn 'sport = :22' 2>/dev/null | grep -q .
  else
    (exec 3<>"/dev/tcp/127.0.0.1/22") 2>/dev/null
  fi
}

prep_ssh() {
  if port22_listening; then
    log "ssh server already listening on :22"
    return 0
  fi
  log "no ssh server on :22 — installing openssh-server"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y || warn "apt-get update failed; continuing"
    DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server \
      || warn "openssh-server install failed; install it manually"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y openssh-server || warn "openssh-server install failed; install it manually"
  else
    warn "neither apt-get nor dnf found; install an ssh server manually"
  fi
  if ! systemctl enable --now ssh >/dev/null 2>&1 \
      && ! systemctl enable --now sshd >/dev/null 2>&1; then
    warn "could not enable sshd (tried units 'ssh' and 'sshd'); key login fails until sshd runs"
  else
    log "sshd enabled and started"
  fi
}

prep_key() {
  if ! command -v ssh-keygen >/dev/null 2>&1; then
    log "installing ssh-keygen (openssh client)"
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update -y || true
      apt-get install -y openssh-client || true
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y openssh-clients || true
    fi
  fi
  command -v ssh-keygen >/dev/null 2>&1 \
    || die "ssh-keygen not available (install openssh-client / openssh-clients)"

  local key="${DATA_DIR}/id_ed25519"
  if [[ ! -f $key ]]; then
    ssh-keygen -t ed25519 -N '' -f "$key" -C vpstui-server -q
  fi
  chown vpstui:vpstui "$key" "${key}.pub"
  chmod 0600 "$key"

  local pub body
  pub=$(tr -d '\n' <"${key}.pub")
  body=$(printf '%s' "$pub" | awk '{print $2}')   # base64 payload, unique per key

  install -d -m 0700 /root/.ssh
  touch /root/.ssh/authorized_keys
  chmod 0600 /root/.ssh/authorized_keys
  if [[ -n $body ]] && grep -qF "$body" /root/.ssh/authorized_keys; then
    log "loopback key already authorized for root"
  else
    printf 'from="127.0.0.1",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc %s\n' \
      "$pub" >> /root/.ssh/authorized_keys
    log "authorized loopback key for root@127.0.0.1 (restricted to 127.0.0.1)"
  fi

  mkdir -p /opt/vpstui
  log "deployment base directory ready: /opt/vpstui"
}

register_server() {
  local srv_name
  srv_name=$(hostname)

  api_get /api/servers "$BEARER"
  if [[ $API_STATUS == 200 ]] \
      && printf '%s' "$API_BODY" | grep -q "\"name\": *\"${srv_name}\""; then
    log "managed server '${srv_name}' already registered — leaving it untouched"
    REGISTERED=already
    return 0
  fi

  local payload
  payload="{\"name\":\"${srv_name}\",\"address\":\"127.0.0.1\",\"port\":22,\"user\":\"root\",\"authType\":\"key\",\"keyPath\":\"${DATA_DIR}/id_ed25519\",\"baseDir\":\"/opt/vpstui\",\"proxyHttpPort\":80,\"proxyHttpsPort\":443}"
  api_post /api/servers "$payload" "$BEARER"
  if [[ $API_STATUS != 201 ]]; then
    warn "server registration failed (HTTP ${API_STATUS}): ${API_BODY}"
    REGISTERED=failed
    return 0
  fi

  local id
  id=$(json_field id)
  if [[ -z $id ]]; then
    warn "registration response did not contain a server id: ${API_BODY}"
    REGISTERED=failed
    return 0
  fi

  api_post "/api/servers/${id}/test" '' "$BEARER"
  if [[ $API_STATUS == 200 ]] && printf '%s' "$API_BODY" | grep -q '"ok": *true'; then
    log "managed server '${srv_name}' registered and connectivity test passed"
    REGISTERED=yes
  else
    warn "connectivity test failed: ${API_BODY}"
    warn "typical cause: root key login is disabled on this machine."
    warn "fix: set 'PermitRootLogin prohibit-password' in /etc/ssh/sshd_config and restart sshd,"
    warn "or register this machine from the GUI with another user; the install itself is complete."
    REGISTERED=failed
  fi
}

vps_prep() {
  prep_docker
  prep_ssh
  prep_key
  if [[ -n $BEARER ]]; then
    register_server
  else
    warn "skipping server registration: no admin session (already configured install)."
    warn "register the server from the GUI, or re-run with --admin-email/--admin-password of the existing admin."
    REGISTERED=skipped
  fi
}

# ------------------------------------------------------------------ summary

print_summary() {
  local host_ip
  host_ip=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
  [[ -n ${host_ip:-} ]] || host_ip=$(hostname)

  local reg_line key_line mode_line
  if (( CONFIGURED )); then
    mode_line="upgrade in place (existing data/admin/servers untouched)"
  else
    mode_line="fresh install"
  fi
  case "$REGISTERED" in
    yes)     reg_line="managed server registered + connectivity test passed" ;;
    already) reg_line="managed server already registered (untouched)" ;;
    failed)  reg_line="managed server registered, connectivity test FAILED (see warnings above)" ;;
    skipped) reg_line="server registration skipped (no admin session)" ;;
    *)       reg_line="server registration skipped (--no-docker)" ;;
  esac
  if [[ -n $API_KEY ]]; then
    key_line="$API_KEY"
  else
    key_line="<not created: already configured>"
  fi

  cat <<EOF

=============================================================
 vpstui-server ${VERSION} is installed and running
=============================================================
 Mode:      ${mode_line}
 Daemon:    http://127.0.0.1:${PORT}  (bound to localhost only)
 First access (from your machine):
     ssh -N -L ${PORT}:127.0.0.1:${PORT} root@${host_ip}
     then open http://127.0.0.1:${PORT}/ in your browser
 Admin:     ${ADMIN_EMAIL}
 Password:  ${ADMIN_PASSWORD}
            (generated this run if you did not pass --admin-password;
             change it under Settings after first login)
 API key:   ${key_line}
            (shown once; use it as 'Authorization: Bearer ...')
 Docker:    $( (( WITH_DOCKER )) && echo "prepared for hosting deployments" || echo "skipped (--no-docker)" )
 Servers:   ${reg_line}
=============================================================
EOF
}

# --------------------------------------------------------------------- main

main() {
  parse_args "$@"
  preflight
  download_release
  install_binary
  write_unit
  activate
  wait_health
  bootstrap_admin
  if (( WITH_DOCKER )); then
    vps_prep
  fi
  print_summary
}

main "$@"
