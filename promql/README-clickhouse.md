# PromQL compliance testing against ClickHouse

This adds a **ClickHouse** target for the PromQL compliance tester (`promql-compliance-tester`), comparing **reference Prometheus** (port **9090**) with ClickHouse’s **Prometheus-compatible query API** on the **host** port (**19093** by default; mapped to **9093** inside the container).

Upstream docs: [promql/README.md](./README.md).

## Layout

| Path | Purpose |
|------|---------|
| [test-clickhouse.yml](./test-clickhouse.yml) | `query_url` for reference vs ClickHouse (base URL only; no `/api/v1/query` suffix). |
| [prometheus-test-data-clickhouse.yml](./prometheus-test-data-clickhouse.yml) | Demo scrape targets + `remote_write` to ClickHouse `/write` on the host port. |
| [clickhouse-docker/](./clickhouse-docker/) | `docker compose` stack, XML for `prometheus` HTTP + `TimeSeries` profile. |
| [scripts/run-on-vm-default-ports.sh](./scripts/run-on-vm-default-ports.sh) | One-shot: compose up → `CREATE TABLE` → Prometheus on **9090** → run tester (committed YAML, no `sed`). |

## Default ports (canonical)

| Role | Host | Notes |
|------|------|--------|
| Reference Prometheus | **9090** | `prometheus --config.file=prometheus-test-data-clickhouse.yml` uses default web listen (`test-clickhouse.yml` → `http://localhost:9090`). |
| ClickHouse prom API + `/write` | **19093** | `docker compose` publishes container **9093** as **19093** (`CLICKHOUSE_PROMETHEUS_HOST_PORT` default). |

With defaults you **do not** copy YAML through `sed`: `remote_write` in [prometheus-test-data-clickhouse.yml](./prometheus-test-data-clickhouse.yml) is already `http://127.0.0.1:19093/write`, and [test-clickhouse.yml](./test-clickhouse.yml) already points at `http://localhost:19093`.

### One-liner on the VM (defaults)

From the **`promql/`** directory of this repo:

```bash
bash scripts/run-on-vm-default-ports.sh
```

Optional extra flags are forwarded to `promql-compliance-tester` (for example `-query-parallelism=8`). Logs: **`/tmp/prom_ch_compliance_default.log`**.

### Manual steps (same as the script)

1. `cd clickhouse-docker && docker compose up -d`
2. `docker exec clickhouse-promql-compliance clickhouse-client -q "CREATE TABLE IF NOT EXISTS default.prometheus ENGINE = TimeSeries"`
3. From `promql/`: `prometheus --config.file=prometheus-test-data-clickhouse.yml` (leave running; default **9090**)
4. After warmup: `go build -o promql-compliance-tester ./cmd/promql-compliance-tester` then  
   `./promql-compliance-tester -config-file=promql-test-queries.yml -config-file=test-clickhouse.yml`

## Port alignment (non-default)

Default **host** port **19093** is controlled by **`CLICKHOUSE_PROMETHEUS_HOST_PORT`** in `docker compose`.

If you change it, update **both** `prometheus-test-data-clickhouse.yml` (`remote_write` URL) and `test-clickhouse.yml` (`test_target_config.query_url`) to the same host port.

## Prerequisites

- **Docker** (for default ClickHouse runtime).
- **Prometheus** binary on the host (reference server on **9090**).
- **Go** (build `promql-compliance-tester`).

## 1. Start ClickHouse (default: Hub `latest`)

From `promql/clickhouse-docker/`:

```bash
docker compose up -d
```

Image default: `clickhouse/clickhouse-server:latest`. **`latest` may trail `master`**; for a build from source, see [Source-built image](#source-built-image) below.

Compose sets **`CLICKHOUSE_SKIP_USER_SETUP=1`** so the `default` user accepts connections from outside the container (required for `remote_write` from host Prometheus). **Use only on isolated test hosts**, not production.

Only the Prometheus handler port is published on the host. Use `docker exec clickhouse-promql-compliance clickhouse-client` for SQL.

## 2. Create the TimeSeries table

Once the container is healthy:

```bash
docker exec clickhouse-promql-compliance clickhouse-client -q \
  "CREATE TABLE IF NOT EXISTS default.prometheus ENGINE = TimeSeries"
```

The experimental flag is enabled via `users.d/time-series-compliance.xml` for the default profile.

## 3. Run reference Prometheus with remote_write

From `promql/`:

```bash
prometheus --config.file=prometheus-test-data-clickhouse.yml
```

Leave it running. Allow **~1 hour** of overlap with demo targets for dense range-query data (shorter runs may show more mismatches). See upstream README for [local demo containers](https://github.com/prometheus/compliance/blob/main/promql/README.md) if `demo.promlabs.com` is unreachable.

## 4. Build and run the tester

From `promql/`:

```bash
go build -o promql-compliance-tester ./cmd/promql-compliance-tester
./promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file=test-clickhouse.yml
```

Example log capture:

```bash
mkdir -p ~/compliance/promql
./promql-compliance-tester \
  -config-file=promql-test-queries.yml \
  -config-file=test-clickhouse.yml \
  2>&1 | tee ~/compliance/promql/promql-clickhouse.log
```

## Source-built image

Same compose and mounts; only the image changes.

1. Build on the VM from [ClickHouse `docker/server`](https://github.com/ClickHouse/ClickHouse/tree/master/docker/server), e.g. `docker build -t ch-promql:local .`
2. Either:
   - `CLICKHOUSE_IMAGE=ch-promql:local docker compose up -d`, or
   - Copy `docker-compose.override.source.yml.example` to `docker-compose.override.yml` and set `image:` to your tag.

Keep **`remote_write`** and **`test_target_config.query_url`** aligned with **`CLICKHOUSE_PROMETHEUS_HOST_PORT`** (default **19093**).

## HTTP basic auth

If you enable auth on the ClickHouse HTTP/prometheus listener, set `basic_auth_user` / `basic_auth_pass` in `test-clickhouse.yml` to match.

## Troubleshooting

- **`Bind … 19093 … already in use`:** another process (often a **native** `clickhouse-server` bound to `127.0.0.1:19093`) is using the default host port. Stop it or set **`CLICKHOUSE_PROMETHEUS_HOST_PORT`** to a free port and edit both YAML files to match.
- **`Bind … 9090 … already in use`:** run reference Prometheus with `--web.listen-address=127.0.0.1:<port>` and set `reference_target_config.query_url` in `test-clickhouse.yml` to `http://localhost:<port>`.
- **`401 Unauthorized` on `remote_write`:** ensure compose includes **`CLICKHOUSE_SKIP_USER_SETUP=1`** (committed default). Without it, the official image restricts the `default` user to loopback while scrapes arrive from the Docker bridge / host network.

## Expectations

First runs may not be 100% passing until `query_tweaks` and/or engine gaps are addressed; this setup is meant to give a **reproducible** baseline.

On this fork branch, `promql-compliance-tester` **records** internal compare errors (for example when the reference Prometheus version no longer matches a testcase’s `should_fail` expectation) as failed results so the run still prints a **`Total: … passed`** summary instead of exiting early.

### Reference Prometheus must match this data path

`test-clickhouse.yml` assumes the **same** reference Prometheus that loads `prometheus-test-data-clickhouse.yml` (demo scrape + `remote_write` into ClickHouse). If **`http://localhost:9090`** is already some other Prometheus on the host, use a different listen address and update `reference_target_config.query_url`.

### Measured pass rate (informative)

With **`clickhouse/clickhouse-server:latest`**, a dedicated reference Prometheus using the same scrape + `remote_write` config as this tree, and a short warmup before the suite, recent runs reported on the order of **`Total: 4 / 539 (~0.7%) passed, 0 unsupported`**, with most failures from ClickHouse’s PromQL **`query_range`** path vs the Go `prometheus` client. Re-run after engine fixes; use a **longer** warmup (upstream suggests ~1 hour) if you care about data-dependent mismatches.
