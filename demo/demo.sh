#!/bin/sh
# Demo environment manager.
# Usage: ./demo.sh up | down | record [theme]
#
# Manages fake service containers and two tori agents (prod + staging)
# for TUI showcase and VHS recording.
set -e

cd "$(dirname "$0")"

PROJECTS="webapp analytics proxy monitoring jobs"
PID_FILE="/tmp/tori-demo.pids"
TORI_BIN="../tori"

up() {
    # Start all compose projects.
    for p in $PROJECTS; do
        docker compose -f "$p.yml" up -d
    done

    # Build tori binary.
    echo "Building tori..."
    make -C .. build

    # Create temp dirs.
    mkdir -p /tmp/tori-prod /tmp/tori-staging

    # Remove stale sockets.
    rm -f /tmp/tori-prod/tori.sock /tmp/tori-staging/tori.sock

    # Start both agents (logs go to temp dirs).
    $TORI_BIN agent --config agent-prod.toml > /tmp/tori-prod/agent.log 2>&1 &
    PROD_PID=$!
    $TORI_BIN agent --config agent-staging.toml > /tmp/tori-staging/agent.log 2>&1 &
    STAGING_PID=$!
    echo "$PROD_PID $STAGING_PID" > "$PID_FILE"

    # Wait for sockets.
    echo "Waiting for agent sockets..."
    for sock in /tmp/tori-prod/tori.sock /tmp/tori-staging/tori.sock; do
        i=0
        while [ ! -S "$sock" ]; do
            i=$((i + 1))
            if [ "$i" -ge 30 ]; then
                echo "Timed out waiting for $sock" >&2
                exit 1
            fi
            sleep 1
        done
    done

    echo "Demo running (PIDs: $PROD_PID, $STAGING_PID)."
    echo "  Connect: tori --config demo/client.toml"
}

down() {
    # Kill agents and wait for them to exit.
    if [ -f "$PID_FILE" ]; then
        for pid in $(cat "$PID_FILE"); do
            kill "$pid" 2>/dev/null || true
        done
        for pid in $(cat "$PID_FILE"); do
            while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done
        done
        rm -f "$PID_FILE"
    fi

    # Tear down compose projects.
    for p in $PROJECTS; do
        docker compose -f "$p.yml" down -v 2>/dev/null || true
    done

    # Clean up temp dirs.
    rm -rf /tmp/tori-prod /tmp/tori-staging
}

record() {
    if ! command -v vhs >/dev/null 2>&1; then
        echo "VHS is not installed. Install it from https://github.com/charmbracelet/vhs" >&2
        exit 1
    fi

    if [ ! -S /tmp/tori-prod/tori.sock ] || [ ! -S /tmp/tori-staging/tori.sock ]; then
        echo "Agents not running. Run './demo.sh up' first and wait ~90s for alerts to fire." >&2
        exit 1
    fi

    THEME="${1:-osaka-jade}"
    TAPE="themes/$THEME.tape"
    if [ ! -f "$TAPE" ]; then
        echo "Unknown theme '$THEME'. Available themes:" >&2
        for f in themes/*.tape; do
            basename "$f" .tape
        done >&2
        exit 1
    fi

    # Isolate VHS from the user's real config. Create a temp XDG dir
    # with the demo client config + theme-specific color overrides so
    # tori renders with explicit hex colors instead of ANSI numbers.
    VHS_CONFIG_DIR=$(mktemp -d)
    mkdir -p "$VHS_CONFIG_DIR/tori"
    cp client.toml "$VHS_CONFIG_DIR/tori/config.toml"
    if [ -f "themes/${THEME}.theme.toml" ]; then
        cat "themes/${THEME}.theme.toml" >> "$VHS_CONFIG_DIR/tori/config.toml"
    fi

    echo "Recording with theme '$THEME'..."
    XDG_CONFIG_HOME="$VHS_CONFIG_DIR" vhs "$TAPE"
    rm -rf "$VHS_CONFIG_DIR"

    # Boost saturation on the generated files.
    WEBM="tori-demo-${THEME}.webm"
    GIF="tori-demo-${THEME}.gif"
    if [ -f "$WEBM" ]; then
        ffmpeg -y -i "$WEBM" -vf "format=yuv420p,eq=saturation=1.3" -c:v libvpx-vp9 -crf 10 -b:v 0 -f webm "${WEBM}.tmp" && mv "${WEBM}.tmp" "$WEBM"
    fi
    if [ -f "$GIF" ]; then
        ffmpeg -y -i "$GIF" -vf "format=rgb24,eq=saturation=1.3,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse" -loop 0 -f gif "${GIF}.tmp" && mv "${GIF}.tmp" "$GIF"
    fi
}

case "${1:-}" in
    up)      up ;;
    down)    down ;;
    record)  record "$2" ;;
    *)
        echo "Usage: $0 {up|down|record [theme]}" >&2
        echo "Themes: osaka-jade (default), tokyo-night, rose-pine" >&2
        exit 1
        ;;
esac
