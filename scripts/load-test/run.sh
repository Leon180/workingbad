#!/usr/bin/env bash
# Vegeta-driven load test harness (issue #30).
#
# Spins up a workingbad serve against a temp DB, fires four
# constant-rate scenarios at it, parses the JSON reports into a
# single-page latency summary, and exits non-zero if any scenario
# breaches its p99 threshold (read=200ms, graph=500ms; write/mixed
# advisory only, the SQLite single-writer ceiling is a known constraint).
#
# Why constant-rate (vegeta) over concurrent-user (k6/oha): SQLite's
# single-writer write-lock contention only surfaces under sustained
# request injection. The concurrent-user model collapses to the
# write-lock ceiling and lies about latency.

set -euo pipefail

BIN=${BIN:-./workingbad}
PORT=${PORT:-7891}                # default off-band so it doesn't fight serve
DIR=${LOAD_TEST_DIR:-/tmp/workingbad-loadtest}
URL="http://127.0.0.1:$PORT"

# Thresholds — match docs/PERF.md p99 commitments.
P99_READ_MS=${P99_READ_MS:-200}
P99_GRAPH_MS=${P99_GRAPH_MS:-500}

# ---------------------------------------------------------------------------
# 0. Preconditions
# ---------------------------------------------------------------------------

if ! command -v vegeta >/dev/null; then
    echo "vegeta not installed. install with:"
    echo "  go install github.com/tsenart/vegeta/v12@latest"
    exit 1
fi
if [ ! -x "$BIN" ]; then
    echo "binary not found at $BIN — run 'make build' first" >&2
    exit 1
fi
if ! command -v python3 >/dev/null; then
    echo "python3 required for the summary parser" >&2
    exit 1
fi

rm -rf "$DIR" && mkdir -p "$DIR"

cat > "$DIR/config.yaml" <<EOF
db:
  path: $DIR/db.sqlite
ai:
  kind: local
  local:
    endpoint: http://localhost:11434
    model: llama3.1
web:
  port: $PORT
EOF

# ---------------------------------------------------------------------------
# 1. Boot the server, trap-clean on exit
# ---------------------------------------------------------------------------

"$BIN" --config "$DIR/config.yaml" serve > "$DIR/server.log" 2>&1 &
SERVE_PID=$!

cleanup() {
    if kill -0 "$SERVE_PID" 2>/dev/null; then
        kill -TERM "$SERVE_PID" 2>/dev/null || true
        wait "$SERVE_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

# Wait until the server answers /healthz (5s budget).
for _ in $(seq 1 20); do
    if curl -sf "$URL/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
if ! curl -sf "$URL/healthz" >/dev/null 2>&1; then
    echo "server did not come up; last 20 lines of log:" >&2
    tail -20 "$DIR/server.log" >&2
    exit 1
fi
echo "server up on $URL"

# Seed a handful of goals so reads of /?type=goal aren't an empty list.
for i in $(seq 1 10); do
    curl -s -X POST -d "title=load-test-goal-$i&body=seed" \
         "$URL/new/goal" -o /dev/null
done

# ---------------------------------------------------------------------------
# 2. Write-storm target file — unique bodies so the SourceRef dedupe check
#    doesn't reject every duplicate. 200 unique payloads is enough cycle
#    headroom for a 100 req/s × 10s storm.
# ---------------------------------------------------------------------------

> "$DIR/write-targets.txt"
for i in $(seq 1 200); do
    body_file="$DIR/body-$i.txt"
    printf 'title=storm-%d&body=load-test-payload-%d-%s' \
        "$i" "$i" "$(date +%s%N)" > "$body_file"
    {
        echo "POST $URL/new/research"
        echo "Content-Type: application/x-www-form-urlencoded"
        echo "@$body_file"
        echo
    } >> "$DIR/write-targets.txt"
done

# Mixed scenario: alternating read + write (also uses unique payloads).
> "$DIR/mixed-targets.txt"
for i in $(seq 1 100); do
    body_file="$DIR/mixed-body-$i.txt"
    printf 'title=mixed-%d&body=mixed-payload-%d' "$i" "$i" > "$body_file"
    {
        echo "GET $URL/?type=goal"
        echo
        echo "POST $URL/new/research"
        echo "Content-Type: application/x-www-form-urlencoded"
        echo "@$body_file"
        echo
    } >> "$DIR/mixed-targets.txt"
done

# ---------------------------------------------------------------------------
# 3. Run scenarios — durations dialed down a bit from the issue
#    (30s → 10s for reads/mixed/graph) to keep CI under 2 minutes per run.
# ---------------------------------------------------------------------------

run_scenario() {
    local name=$1 rate=$2 duration=$3 targets=$4
    echo "--- $name @ ${rate} req/s for ${duration} ---"
    vegeta attack -rate="$rate" -duration="$duration" -targets="$targets" \
        | vegeta report -type=json > "$DIR/${name}.json"
}

# 1. Read-heavy (the SELECT path)
echo "GET $URL/?type=goal" > "$DIR/read-targets.txt"
run_scenario read 200 10s "$DIR/read-targets.txt"

# 2. Mixed (read + write under the same wall clock — SQLite contention surface)
run_scenario mixed 50 10s "$DIR/mixed-targets.txt"

# 3. Write storm (the SQLite single-writer ceiling)
run_scenario write 100 5s "$DIR/write-targets.txt"

# 4. Graph heavy (layout algorithm + DB)
echo "GET $URL/graph" > "$DIR/graph-targets.txt"
run_scenario graph 50 10s "$DIR/graph-targets.txt"

# ---------------------------------------------------------------------------
# 4. Summary table + threshold gate
# ---------------------------------------------------------------------------

python3 - "$DIR" "$P99_READ_MS" "$P99_GRAPH_MS" <<'PY'
import json, sys
out_dir, p99_read, p99_graph = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
scenarios = [("read", "200 req/s × 10s", p99_read),
             ("mixed", "50 req/s × 10s", None),
             ("write", "100 req/s × 5s", None),
             ("graph", "50 req/s × 10s", p99_graph)]

print()
print(f"{'scenario':<10} {'shape':<22} {'p50':>9} {'p99':>9} {'success':>9}  {'gate':<14}")
print("-" * 80)
fails = 0
for name, shape, limit in scenarios:
    with open(f"{out_dir}/{name}.json") as fh:
        d = json.load(fh)
    p50 = d["latencies"]["50th"] / 1e6
    p99 = d["latencies"]["99th"] / 1e6
    success = d["success"] * 100
    if limit is None:
        gate = "advisory"
    elif p99 > limit:
        gate = f"FAIL >{limit}ms"
        fails += 1
    else:
        gate = f"OK  <{limit}ms"
    print(f"{name:<10} {shape:<22} {p50:>7.1f}ms {p99:>7.1f}ms {success:>8.2f}%  {gate:<14}")

print()
if fails:
    print(f"FAIL: {fails} scenario(s) breached their p99 gate")
    sys.exit(1)
print("OK — all gated scenarios within budget")
PY
