#!/bin/sh
# Demo environment manager.
# Usage: ./demo.sh up | ./demo.sh down
#
# Starts fake service containers for TUI showcase. Run the agent separately:
#   mkdir -p /tmp/tori
#   tori agent --config demo/config.toml
#   tori -socket /tmp/tori/tori.sock
set -e

cd "$(dirname "$0")"

PROJECTS="webapp analytics proxy monitoring jobs"

case "${1:-up}" in
    up)
        for p in $PROJECTS; do
            docker compose -f "$p.yml" up -d
        done
        echo "Demo running."
        echo "  Agent:   mkdir -p /tmp/tori && tori agent --config demo/config.toml"
        echo "  Connect: tori -socket /tmp/tori/tori.sock"
        ;;
    down)
        for p in $PROJECTS; do
            docker compose -f "$p.yml" down -v
        done
        ;;
    *)
        echo "Usage: $0 {up|down}" >&2
        exit 1
        ;;
esac
