#!/usr/bin/env bash
# Run the full PromQL compliance flow on a Linux host using committed default ports:
#   Reference Prometheus:  http://localhost:9090  (default listen)
#   ClickHouse prom HTTP: http://localhost:19093  (docker compose host map → container 9093)
#
# Prerequisites: Docker, prometheus binary, Go; ports 9090 and 19093 free on the host.
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

go build -o /tmp/promql-compliance-tester ./cmd/promql-compliance-tester
/tmp/promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file=test-clickhouse.yml \
  "$@"
