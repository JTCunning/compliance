#!/usr/bin/env bash
# Run the full PromQL compliance flow on a Linux host using committed default ports:
#   Reference Prometheus:  http://localhost:9090  (default listen)
#   ClickHouse prom HTTP: http://localhost:19093  (docker compose host map → container 9093)
#
# By default this starts a small Python sidecar on :29193 that rewrites client_golang's
# POST /api/v1/query_range (and /query) into GET with the same form fields, because
# prometheus/client_golang has no env flag to force GET. Set PROMQL_COMPLIANCE_NO_GET_PROXY=1
# to talk to ClickHouse directly with test-clickhouse.yml (POST).
#
# Prerequisites: Docker, prometheus binary, Go, python3; ports 9090, 19093, 29193 free.
# Usage (from anywhere):  bash /path/to/compliance/promql/scripts/run-on-vm-default-ports.sh
# Or from promql/:        bash scripts/run-on-vm-default-ports.sh

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/clickhouse-docker"
docker compose up -d
sleep 12
docker exec clickhouse-promql-compliance clickhouse-client -q \
  "CREATE TABLE IF NOT EXISTS default.prometheus ENGINE = TimeSeries"

cd "$ROOT"
# Avoid colliding with an unrelated prometheus; only stop one using our config path.
pkill -f "prometheus.*${ROOT}/prometheus-test-data-clickhouse.yml" 2>/dev/null || true
sleep 1
rm -rf /tmp/prom_ch_compliance_default
mkdir -p /tmp/prom_ch_compliance_default
nohup prometheus \
  --config.file="$ROOT/prometheus-test-data-clickhouse.yml" \
  --storage.tsdb.path=/tmp/prom_ch_compliance_default \
  >> /tmp/prom_ch_compliance_default.log 2>&1 &
sleep 20
curl -sf "http://127.0.0.1:9090/-/healthy" >/dev/null || {
  echo "Prometheus did not become healthy on :9090; see /tmp/prom_ch_compliance_default.log"
  exit 1
}

if [ "${PROMQL_COMPLIANCE_NO_GET_PROXY:-}" = "1" ]; then
  TEST_CFG="test-clickhouse.yml"
else
  pkill -f "prometheus-post-to-get-proxy.py" 2>/dev/null || true
  sleep 1
  export PROMQL_GET_PROXY_UPSTREAM="${PROMQL_GET_PROXY_UPSTREAM:-http://127.0.0.1:19093}"
  export PROMQL_GET_PROXY_LISTEN="${PROMQL_GET_PROXY_LISTEN:-0.0.0.0:29193}"
  nohup python3 "$ROOT/scripts/prometheus-post-to-get-proxy.py" \
    >> /tmp/promql_get_proxy.log 2>&1 &
  sleep 2
  curl -sf "http://127.0.0.1:29193/api/v1/query?query=1" >/dev/null 2>&1 || true
  TEST_CFG="test-clickhouse-get-proxy.yml"
fi

go build -o /tmp/promql-compliance-tester ./cmd/promql-compliance-tester
/tmp/promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file="$TEST_CFG" \
  "$@"
