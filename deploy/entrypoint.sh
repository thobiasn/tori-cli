#!/bin/sh
set -e

# If ROOK_CONFIG is set, write it to the config file.
# This lets PaaS platforms (Dokploy, Coolify, etc.) inject
# the full TOML config as an environment variable.
if [ -n "$ROOK_CONFIG" ]; then
    mkdir -p /etc/rook
    printf '%s\n' "$ROOK_CONFIG" > /etc/rook/config.toml
    chmod 0600 /etc/rook/config.toml
fi

exec "$@"
