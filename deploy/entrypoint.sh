#!/bin/sh
set -e

# If TORI_CONFIG is set, write it to the config file.
# This lets PaaS platforms (Dokploy, Coolify, etc.) inject
# the full TOML config as an environment variable.
if [ -n "$TORI_CONFIG" ]; then
    mkdir -p /etc/tori
    printf '%s\n' "$TORI_CONFIG" > /etc/tori/config.toml
    chmod 0600 /etc/tori/config.toml
fi

exec "$@"
