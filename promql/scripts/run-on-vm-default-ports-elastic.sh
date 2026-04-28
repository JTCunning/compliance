#!/usr/bin/env bash
# Run the full PromQL compliance flow on a Linux host against Elasticsearch TSDS + promql-shim:
#   Reference Prometheus:  http://localhost:${REF_PROM_PORT:-9092}
#   Elasticsearch HTTP:     http://localhost:${ES_HTTP_HOST_PORT:-9200}
#   promql-shim (Prom API): http://localhost:${ES_PROMQL_HOST_PORT:-19094}
#
# Prerequisites: Docker; prometheus binary; Go; ports 9092, 9200, 19094 free (defaults).
# Optional: COMPLIANCE_WARMUP_SECONDS (default 360) — time to scrape + remote_write before the tester.
#
# Usage (from anywhere):  bash /path/to/compliance/promql/scripts/run-on-vm-default-ports-elastic.sh
# Or from promql/:        bash scripts/run-on-vm-default-ports-elastic.sh

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REF_PROM_PORT="${REF_PROM_PORT:-9092}"
ES_HTTP_HOST_PORT="${ES_HTTP_HOST_PORT:-9200}"
ES_PROMQL_HOST_PORT="${ES_PROMQL_HOST_PORT:-19094}"
WARMUP="${COMPLIANCE_WARMUP_SECONDS:-360}"

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
rm -rf /tmp/prom_es_compliance_default
mkdir -p /tmp/prom_es_compliance_default
nohup prometheus \
  --config.file="$ROOT/prometheus-test-data-elastic.yml" \
  --web.listen-address="0.0.0.0:${REF_PROM_PORT}" \
  --storage.tsdb.path=/tmp/prom_es_compliance_default \
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

go build -o /tmp/promql-compliance-tester ./cmd/promql-compliance-tester
/tmp/promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file=test-elastic.yml \
  "$@"
