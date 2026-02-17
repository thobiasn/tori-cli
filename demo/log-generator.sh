#!/bin/sh
# Log generator for demo containers.
# Prints structured JSON logs at random intervals with a mix of levels.
# Usage: SERVICE_TYPE=api ./log-generator.sh
# SERVICE_TYPE controls the field vocabulary (api, worker, db, ingest, scheduler).

SERVICE_TYPE="${SERVICE_TYPE:-generic}"

rand() {
    # Random integer in [0, $1)
    awk "BEGIN{srand(); print int(rand()*$1)}"
}

timestamp() {
    date -u +"%Y-%m-%dT%H:%M:%SZ"
}

# Pick a log level: 70% info, 20% warn, 10% error.
pick_level() {
    r=$(rand 100)
    if [ "$r" -lt 70 ]; then
        echo "info"
    elif [ "$r" -lt 90 ]; then
        echo "warn"
    else
        echo "error"
    fi
}

log_api() {
    level=$(pick_level)
    ts=$(timestamp)
    case "$level" in
        info)
            methods="GET GET GET POST PUT DELETE"
            method=$(echo $methods | tr ' ' '\n' | awk "NR==$(( $(rand 6) + 1 ))")
            paths="/api/users /api/orders /api/products /api/health /api/sessions"
            path=$(echo $paths | tr ' ' '\n' | awk "NR==$(( $(rand 5) + 1 ))")
            status=$(echo "200 200 200 200 200 200 200 200 201 204 304 401 401 403" | tr ' ' '\n' | awk "NR==$(( $(rand 14) + 1 ))")
            latency=$(( $(rand 180) + 5 ))
            printf '{"time":"%s","level":"info","msg":"request completed","method":"%s","path":"%s","status":%s,"latency_ms":%d}\n' \
                "$ts" "$method" "$path" "$status" "$latency"
            ;;
        warn)
            r=$(rand 3)
            case "$r" in
                0) printf '{"time":"%s","level":"warn","msg":"slow query detected","query":"SELECT * FROM orders WHERE ...","duration_ms":%d}\n' "$ts" "$(( $(rand 3000) + 500 ))" ;;
                1) printf '{"time":"%s","level":"warn","msg":"high connection pool usage","pool":"primary","active_pct":%d}\n' "$ts" "$(( $(rand 30) + 70 ))" ;;
                2) printf '{"time":"%s","level":"warn","msg":"request timeout approaching","path":"/api/reports","elapsed_ms":%d}\n' "$ts" "$(( $(rand 2000) + 3000 ))" ;;
            esac
            ;;
        error)
            r=$(rand 3)
            case "$r" in
                0) printf '{"time":"%s","level":"error","msg":"connection refused","host":"redis:6379","retry":%d}\n' "$ts" "$(( $(rand 5) + 1 ))" ;;
                1) printf '{"time":"%s","level":"error","msg":"upstream returned 502","host":"api-gateway:8080","retry":%d}\n' "$ts" "$(( $(rand 5) + 1 ))" ;;
                2) printf '{"time":"%s","level":"error","msg":"TLS handshake timeout","host":"auth.internal:443","retry":%d}\n' "$ts" "$(( $(rand 5) + 1 ))" ;;
            esac
            ;;
    esac
}

log_worker() {
    level=$(pick_level)
    ts=$(timestamp)
    case "$level" in
        info)
            queues="email notifications reports imports webhooks"
            queue=$(echo $queues | tr ' ' '\n' | awk "NR==$(( $(rand 5) + 1 ))")
            duration=$(( $(rand 2000) + 100 ))
            printf '{"time":"%s","level":"info","msg":"job completed","queue":"%s","duration_ms":%d,"jobs_pending":%d}\n' \
                "$ts" "$queue" "$duration" "$(rand 50)"
            ;;
        warn)
            printf '{"time":"%s","level":"warn","msg":"job retry scheduled","queue":"email","attempt":%d,"max_retries":5}\n' \
                "$ts" "$(( $(rand 4) + 2 ))"
            ;;
        error)
            printf '{"time":"%s","level":"error","msg":"job failed permanently","queue":"reports","error":"deadline exceeded","job_id":"job_%d"}\n' \
                "$ts" "$(rand 99999)"
            ;;
    esac
}

log_ingest() {
    level=$(pick_level)
    ts=$(timestamp)
    case "$level" in
        info)
            batch=$(( $(rand 5000) + 500 ))
            printf '{"time":"%s","level":"info","msg":"batch ingested","records":%d,"source":"kafka","partition":%d}\n' \
                "$ts" "$batch" "$(rand 12)"
            ;;
        warn)
            printf '{"time":"%s","level":"warn","msg":"ingestion lag increasing","lag_ms":%d,"partition":%d}\n' \
                "$ts" "$(( $(rand 5000) + 1000 ))" "$(rand 12)"
            ;;
        error)
            printf '{"time":"%s","level":"error","msg":"schema validation failed","records_dropped":%d,"reason":"missing required field"}\n' \
                "$ts" "$(( $(rand 20) + 1 ))"
            ;;
    esac
}

log_scheduler() {
    level=$(pick_level)
    ts=$(timestamp)
    case "$level" in
        info)
            tasks="cleanup-old-sessions generate-daily-report sync-inventory expire-tokens archive-logs"
            task=$(echo $tasks | tr ' ' '\n' | awk "NR==$(( $(rand 5) + 1 ))")
            printf '{"time":"%s","level":"info","msg":"task executed","task":"%s","duration_ms":%d,"next_run_in":"60s"}\n' \
                "$ts" "$task" "$(( $(rand 500) + 10 ))"
            ;;
        warn)
            printf '{"time":"%s","level":"warn","msg":"task execution slow","task":"generate-daily-report","duration_ms":%d,"threshold_ms":5000}\n' \
                "$ts" "$(( $(rand 8000) + 5000 ))"
            ;;
        error)
            printf '{"time":"%s","level":"error","msg":"task failed","task":"sync-inventory","error":"connection reset by peer"}\n' "$ts"
            ;;
    esac
}

log_generic() {
    level=$(pick_level)
    ts=$(timestamp)
    case "$level" in
        info)  printf '{"time":"%s","level":"info","msg":"heartbeat","status":"ok"}\n' "$ts" ;;
        warn)  printf '{"time":"%s","level":"warn","msg":"high latency detected","latency_ms":%d}\n' "$ts" "$(( $(rand 500) + 200 ))" ;;
        error) printf '{"time":"%s","level":"error","msg":"unexpected error","code":"INTERNAL"}\n' "$ts" ;;
    esac
}

# Main loop
while true; do
    case "$SERVICE_TYPE" in
        api)       log_api ;;
        worker)    log_worker ;;
        ingest)    log_ingest ;;
        scheduler) log_scheduler ;;
        *)         log_generic ;;
    esac

    # Random sleep between 0.5s and 3s
    # Use awk since sleep in alpine supports fractional seconds
    delay=$(awk "BEGIN{srand(); printf \"%.1f\", 0.5 + rand()*2.5}")
    sleep "$delay"
done
