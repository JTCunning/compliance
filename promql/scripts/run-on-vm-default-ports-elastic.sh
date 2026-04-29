#!/usr/bin/env bash
# Run the full PromQL compliance flow on a Linux host against Elasticsearch TSDS + promql-shim:
#   Reference Prometheus:  http://localhost:${REF_PROM_PORT:-9092}
#   Elasticsearch HTTP:     http://localhost:${ES_HTTP_HOST_PORT:-9200}
#   promql-shim (Prom API): http://localhost:${ES_PROMQL_HOST_PORT:-19094}
#
# Prerequisites: Docker; prometheus binary; Go; ports 9092, 9200, 19094 free (defaults).
# Optional:
#   COMPLIANCE_WARMUP_SECONDS (default 360) — scrape + remote_write before the tester (use 3600 for upstream-style 1h warmup).
#   COMPLIANCE_STORAGE_LOG — JSONL path for storage snapshots (default /tmp/prom_es_benchmark_storage.jsonl).
#   PROMETHEUS_TSDB_PATH — reference Prometheus TSDB dir (default /tmp/prom_es_compliance_default).
#   COMPLIANCE_JSON_RESULTS — if set, writes per-query comparison JSON (-output-format json; last flag wins, so pass tester flags before any conflicting -output-format).
#
# Usage (from anywhere):  bash /path/to/compliance/promql/scripts/run-on-vm-default-ports-elastic.sh
# Or from promql/:        bash scripts/run-on-vm-default-ports-elastic.sh

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REF_PROM_PORT="${REF_PROM_PORT:-9092}"
ES_HTTP_HOST_PORT="${ES_HTTP_HOST_PORT:-9200}"
ES_PROMQL_HOST_PORT="${ES_PROMQL_HOST_PORT:-19094}"
WARMUP="${COMPLIANCE_WARMUP_SECONDS:-360}"
PROM_TSDB="${PROMETHEUS_TSDB_PATH:-/tmp/prom_es_compliance_default}"
STORAGE_LOG="${COMPLIANCE_STORAGE_LOG:-/tmp/prom_es_benchmark_storage.jsonl}"
ES_DOCKER_NAME="${ELASTICSEARCH_DOCKER_NAME:-elasticsearch-promql-compliance}"

record_storage_snapshot() {
  local phase="$1"
  python3 "$ROOT/scripts/capture_elastic_benchmark_storage.py" \
    --phase "$phase" \
    --prom-tsdb "$PROM_TSDB" \
    --es-url "http://127.0.0.1:${ES_HTTP_HOST_PORT}" \
    --log "$STORAGE_LOG" \
    --docker-es-container "$ES_DOCKER_NAME"
}

cd "$ROOT/elastic-docker"
docker compose up -d
sleep 15

echo "Waiting for Elasticsearch on :${ES_HTTP_HOST_PORT} ..."
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:${ES_HTTP_HOST_PORT}/_cluster/health?wait_for_status=yellow&timeout=10s" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

echo "Waiting for promql-shim on :${ES_PROMQL_HOST_PORT} ..."
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:${ES_PROMQL_HOST_PORT}/-/healthy" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

cd "$ROOT"
# Avoid colliding with an unrelated prometheus; only stop one using our config path.
pkill -f "prometheus.*${ROOT}/prometheus-test-data-elastic.yml" 2>/dev/null || true
sleep 1
rm -rf "$PROM_TSDB"
mkdir -p "$PROM_TSDB"
nohup prometheus \
  --config.file="$ROOT/prometheus-test-data-elastic.yml" \
  --web.listen-address="0.0.0.0:${REF_PROM_PORT}" \
  --storage.tsdb.path="$PROM_TSDB" \
  >> /tmp/prom_es_compliance_default.log 2>&1 &
for _ in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:${REF_PROM_PORT}/-/healthy" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf "http://127.0.0.1:${REF_PROM_PORT}/-/healthy" >/dev/null || {
  echo "Prometheus did not become healthy on :${REF_PROM_PORT}; see /tmp/prom_es_compliance_default.log"
  exit 1
}

echo "Warmup: scraping + remote_write for ${WARMUP}s (override with COMPLIANCE_WARMUP_SECONDS) ..."
sleep "$WARMUP"

echo "Storage snapshot: after write (before promql-compliance-tester read phase) → ${STORAGE_LOG}"
record_storage_snapshot post_write

curl -sf "http://127.0.0.1:${REF_PROM_PORT}/-/healthy" >/dev/null || {
  echo "ERROR: Reference Prometheus on :${REF_PROM_PORT} is not healthy before the tester (see /tmp/prom_es_compliance_default.log)."
  exit 1
}

go build -o /tmp/promql-compliance-tester ./cmd/promql-compliance-tester
set +e
if [[ -n "${COMPLIANCE_JSON_RESULTS:-}" ]]; then
  /tmp/promql-compliance-tester \
    -config-file=promql-test-queries.yml \
    -config-file=test-elastic.yml \
    "$@" \
    -output-format json >"$COMPLIANCE_JSON_RESULTS"
  _tester_rc=$?
  echo "Per-query ref-vs-test JSON written to: ${COMPLIANCE_JSON_RESULTS}"
  python3 "$ROOT/scripts/summarize_promql_compliance_json.py" "$COMPLIANCE_JSON_RESULTS" || true
else
  /tmp/promql-compliance-tester \
    -config-file=promql-test-queries.yml \
    -config-file=test-elastic.yml \
    "$@"
  _tester_rc=$?
fi
set -e

echo "Storage snapshot: after read (tester finished, rc=${_tester_rc}) → ${STORAGE_LOG}"
record_storage_snapshot post_read

exit "${_tester_rc}"
