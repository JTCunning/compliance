# PromQL compliance testing against Elasticsearch (TSDS + ES|QL `PROMQL`)

This adds an **Elasticsearch** target for the PromQL compliance tester (`promql-compliance-tester`), comparing **reference Prometheus** with Elasticsearch **9.4+** metrics stored in **time-series data streams (TSDS)** and queried through the ES|QL **`PROMQL`** source command.

Upstream docs: [promql/README.md](./README.md).

Elastic background:

- Native **Prometheus Remote Write** receiver on Elasticsearch: [How Prometheus Remote Write Ingestion Works in Elasticsearch](https://www.elastic.co/observability-labs/blog/prometheus-remote-write-elasticsearch-architecture).
- **`PROMQL` in ES|QL** (tech preview in 9.4): [Query Prometheus Metrics in Elasticsearch with Native PromQL Support](https://www.elastic.co/observability-labs/blog/elasticsearch-supports-promql).

## Why a shim (`promql-shim`)

The compliance tester uses `github.com/prometheus/client_golang/api/prometheus/v1` and therefore speaks **`/api/v1/query_range`** with the Prometheus JSON envelope.

Elasticsearch exposes PromQL through **`POST /_query`** with an ES|QL body such as:

```text
PROMQL index=metrics-* step=10s start="…" end="…" (<PromQL expression>)
```

and returns **columnar** JSON (`columns` + `values`), not the Prometheus HTTP API shape.

The **`promql-shim`** sidecar accepts `/api/v1/query_range`, forwards to ES|QL `PROMQL`, and reshapes results into `{"status":"success","data":{"resultType":"matrix","result":[…]}}` so the stock `promql-compliance-tester` binary stays unchanged.

## Layout

| Path | Purpose |
|------|---------|
| [test-elastic.yml](./test-elastic.yml) | `query_url` for reference Prometheus vs `promql-shim` (Prometheus-compatible surface). |
| [prometheus-test-data-elastic.yml](./prometheus-test-data-elastic.yml) | Demo scrape targets + `remote_write` to Elasticsearch `/api/v1/prometheus/_remote_write`. |
| [elastic-docker/](./elastic-docker/) | `docker compose` stack: Elasticsearch, one-shot index-template bootstrap, `promql-shim`. |
| [scripts/run-on-vm-default-ports-elastic.sh](./scripts/run-on-vm-default-ports-elastic.sh) | One-shot: compose up → reference Prometheus on **9092** → warmup → run tester (`test-elastic.yml`). |
| [scripts/capture_elastic_benchmark_storage.py](./scripts/capture_elastic_benchmark_storage.py) | Optional: append one JSON line of **Prometheus TSDB** + **Elasticsearch** sizes (also invoked automatically by the run script). |

## Default ports (canonical)

| Role | Host | Notes |
|------|------|--------|
| Reference Prometheus | **9092** | Started by the script with `--web.listen-address=0.0.0.0:9092` so it does **not** collide with the ClickHouse flow on **9090**. |
| Elasticsearch HTTP | **9200** | `ES_HTTP_HOST_PORT` in `elastic-docker/docker-compose.yml`. |
| promql-shim | **19094** | `ES_PROMQL_HOST_PORT`; maps container `:8080`. |

With defaults you **do not** edit `remote_write` in [prometheus-test-data-elastic.yml](./prometheus-test-data-elastic.yml): it points at `http://127.0.0.1:9200/api/v1/prometheus/_remote_write`, and [test-elastic.yml](./test-elastic.yml) points the test target at `http://localhost:19094`.

If you change published ports, update **both** `prometheus-test-data-elastic.yml` (`remote_write` URL) and `test-elastic.yml` / the script env vars together.

## Elasticsearch Docker image version

The **`PROMQL`** ES|QL command is introduced in **9.4**. On `docker.elastic.co`, the GA tag **`9.4.0` may not exist yet**; this stack defaults to **`9.4.0-SNAPSHOT`** via `ELASTIC_VERSION` in [elastic-docker/docker-compose.yml](./elastic-docker/docker-compose.yml).

When **`docker.elastic.co/elasticsearch/elasticsearch:9.4.0`** is published:

```bash
ELASTIC_VERSION=9.4.0 docker compose -f elastic-docker/docker-compose.yml up -d
```

## Prerequisites

- **Docker** (Compose v2).
- **Prometheus** binary on the host (reference server).
- **Go** (build `promql-compliance-tester`).

## 1. Start Elasticsearch + bootstrap + shim

From **`promql/elastic-docker/`**:

```bash
docker compose up -d
```

The **bootstrap** container installs a TSDS-oriented `_index_template` for `metrics-*` so data matches **`index.mode: time_series`** (required for sensible `PROMQL` defaults).

## 2. Run reference Prometheus with remote_write

From **`promql/`** (or use the script in step 4):

```bash
prometheus \
  --config.file=prometheus-test-data-elastic.yml \
  --web.listen-address=0.0.0.0:9092 \
  --storage.tsdb.path=/tmp/prom_es_manual
```

Leave it running. Allow sufficient **warmup** so both Prometheus and Elasticsearch have overlapping samples for dense `query_range` evaluations (the script defaults to **360s**; override with `COMPLIANCE_WARMUP_SECONDS`).

## 3. Build and run the tester

From **`promql/`**:

```bash
go build -o promql-compliance-tester ./cmd/promql-compliance-tester
./promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file=test-elastic.yml
```

## One-liner (defaults)

From **`promql/`**:

```bash
bash scripts/run-on-vm-default-ports-elastic.sh
```

Optional extra flags are forwarded to `promql-compliance-tester` (for example `-query-parallelism=8`). Logs: **`/tmp/prom_es_compliance_default.log`**.

Shorter local iteration:

```bash
COMPLIANCE_WARMUP_SECONDS=120 bash scripts/run-on-vm-default-ports-elastic.sh
```

Upstream-style **one-hour** warmup (dense overlap for range queries):

```bash
COMPLIANCE_WARMUP_SECONDS=3600 bash scripts/run-on-vm-default-ports-elastic.sh
```

## Storage snapshots (write vs read)

The run script records **on-disk / logical store sizes** for both write paths used in this benchmark:

| System | What is measured |
|--------|------------------|
| **Reference Prometheus** | Host `du -sb` on the TSDB directory (`PROMETHEUS_TSDB_PATH`, default `/tmp/prom_es_compliance_default`). |
| **Elasticsearch** | `/_cluster/stats` → `indices.store.size_in_bytes` and `indices.docs.count`, plus optional `docker exec … du -sb` on `/usr/share/elasticsearch/data` inside the ES container. |

**When:** two JSON lines are appended to **`COMPLIANCE_STORAGE_LOG`** (default **`/tmp/prom_es_benchmark_storage.jsonl`**):

1. **`post_write`** — immediately **after** the warmup sleep (scrapes + `remote_write` to ES have been running), **before** `promql-compliance-tester` (the heavy **read** phase against Prometheus + the shim/ES).
2. **`post_read`** — **after** the tester exits (same stores; sizes usually stable because the suite is read-biased).

Each line is one JSON object (JSONL). Example:

```bash
tail -2 /tmp/prom_es_benchmark_storage.jsonl | python3 -m json.tool
```

Manual capture (same fields):

```bash
python3 scripts/capture_elastic_benchmark_storage.py \
  --phase manual \
  --prom-tsdb /tmp/prom_es_compliance_default \
  --es-url http://127.0.0.1:9200
```

Override the ES container name if yours differs: **`ELASTICSEARCH_DOCKER_NAME`** (default `elasticsearch-promql-compliance`).

## Build only the shim image

From **`promql/`**:

```bash
make docker-elastic-shim
```

## Known Elastic 9.4 tech-preview gaps (expected failures / tweaks)

From Elastic’s PromQL announcement, notable gaps in the initial **`PROMQL`** tech preview include:

- Group modifiers like `on(...) group_left(...)`.
- Binary set operators (`or`, `and`, `unless`).
- Some functions still missing or partial, including **`histogram_quantile`**, **`predict_linear`**, and **`label_join`**.

These usually show up as **query errors** or **matrix mismatches** in the compliance tester. Add **`query_tweaks`** in `test-elastic.yml` only after you have a measured baseline and a systematic difference you want to paper over (same workflow as other vendors).

## Measured pass rate

Example run (Linux VM, `-query-parallelism=20`, `COMPLIANCE_WARMUP_SECONDS=90`):

| Elasticsearch image | Warmup (`COMPLIANCE_WARMUP_SECONDS`) | Result |
|----------------------|--------------------------------------|--------|
| `docker.elastic.co/elasticsearch/elasticsearch:9.4.0-SNAPSHOT` (default in compose) | 90 | **399 / 539 (74.03%) passed, 0 unsupported** — captured from `promql-compliance-tester` summary line. |

Notes:

- That run used an earlier `promql-shim` build; the current shim maps Elasticsearch **`parsing_exception` / “function … does not exist”** responses to **HTTP 501**, so the same engine gaps should surface as **unsupported** in the tester summary (re-run to refresh counts).
- If reference Prometheus flakes with **503** or **connection refused** under heavy parallel queries, increase warmup (`COMPLIANCE_WARMUP_SECONDS`, default **360** in the script) and/or lower `-query-parallelism`.

## Expectations

First runs may not be 100% passing until `query_tweaks` and/or engine gaps are addressed; this setup is meant to give a **reproducible** baseline comparable to the ClickHouse path in [README-clickhouse.md](./README-clickhouse.md).

If **remote_write** returns `404`, confirm the image is **9.4+** and that you are posting to **`/api/v1/prometheus/_remote_write`** (see Elastic’s architecture post).

If **`PROMQL` parse errors** appear, the running build likely predates 9.4 — bump `ELASTIC_VERSION` to a **9.4.0-SNAPSHOT** / **9.4.x** artifact that includes the ES|QL `PROMQL` command.

If **indexing** fails with mapping errors, capture one ingested document (`GET metrics-*/_search`) and adjust [elastic-docker/bootstrap/metrics-tsds-template.json](./elastic-docker/bootstrap/metrics-tsds-template.json) (routing path / dynamic templates) to match the native remote-write field layout for your exact version.
