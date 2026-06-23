#!/bin/bash
# Entrypoint mirroring the mautrix-* bridge containers:
#   - on first run, if there's no config, copy the example and exit with help
#   - if there's no registration, generate it (-g) and exit
#   - otherwise run the bridge
# All persistent state lives in /data (mounted volume).
set -euo pipefail

CONFIG=/data/config.yaml
REGISTRATION=/data/registration.yaml
EXAMPLE=/opt/example-config.yaml

# Fix ownership of the data dir on every start.
if [[ "$(id -u)" == "0" ]]; then
    chown -R "${UID:-1337}:${GID:-1337}" /data 2>/dev/null || true
    exec su-exec "${UID:-1337}:${GID:-1337}" "$0" "$@"
fi

if [[ ! -f "$CONFIG" ]]; then
    if [[ -f "$EXAMPLE" ]]; then
        cp "$EXAMPLE" "$CONFIG"
    fi
    echo "A new config has been written to $CONFIG."
    echo "Edit it (homeserver, network.base_url, permissions), then start again."
    exit 0
fi

if [[ ! -f "$REGISTRATION" ]]; then
    echo "Generating appservice registration..."
    exec mautrix-wechat -c "$CONFIG" -r "$REGISTRATION" -g
fi

exec mautrix-wechat -c "$CONFIG" -r "$REGISTRATION" "$@"
