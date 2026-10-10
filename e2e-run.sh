#!/bin/sh
# e2e-run.sh — launch vpstui ONLY with the throwaway e2e config.
# Refuses to start if the config dir does not exist (never touches real hosts.json).
[ -d /tmp/vpstui-e2e/cfg/vpstui ] || { echo "e2e config missing — aborting" >&2; exit 1; }
cd "$(dirname "$0")" || exit 1
XDG_CONFIG_HOME=/tmp/vpstui-e2e/cfg XDG_DATA_HOME=/tmp/vpstui-e2e/data exec ./vpstui "$@"
