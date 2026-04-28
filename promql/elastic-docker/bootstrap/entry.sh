#!/bin/sh
set -eu
ES="${ES_URL:-http://elasticsearch:9200}"
echo "Waiting for Elasticsearch at ${ES} ..."
i=0
while [ "$i" -lt 90 ]; do
  if curl -sf "${ES}/_cluster/health?wait_for_status=yellow&timeout=30s" >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
curl -sf "${ES}/_cluster/health?wait_for_status=yellow&timeout=30s" >/dev/null || {
  echo "ERROR: Elasticsearch did not become ready"
  exit 1
}

echo "Installing index template for TSDS metrics-* ..."
curl -sf -X PUT "${ES}/_index_template/metrics-prometheus-compliance" \
  -H 'Content-Type: application/json' \
  --data-binary @/bootstrap/metrics-tsds-template.json

echo "Bootstrap complete."
