# PromQL compliance testing against ClickHouse

This adds a **ClickHouse** target for the PromQL compliance tester (`promql-compliance-tester`), comparing **reference Prometheus** (port **9090**) with ClickHouse’s **Prometheus-compatible query API** on port **9093**.

Upstream docs: [promql/README.md](./README.md).

## Layout

| Path | Purpose |
|------|---------|
| [test-clickhouse.yml](./test-clickhouse.yml) | `query_url` for reference vs ClickHouse (base URL only; no `/api/v1/query` suffix). |
| [prometheus-test-data-clickhouse.yml](./prometheus-test-data-clickhouse.yml) | Demo scrape targets + `remote_write` to `http://127.0.0.1:9093/write`. |
| [clickhouse-docker/](./clickhouse-docker/) | `docker compose` stack, XML for `prometheus` HTTP + `TimeSeries` profile. |

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

`remote_write` and `test-clickhouse.yml` URLs stay **9093** on the host.

## HTTP basic auth

If you enable auth on the ClickHouse HTTP/prometheus listener, set `basic_auth_user` / `basic_auth_pass` in `test-clickhouse.yml` to match.

## Expectations

First runs may not be 100% passing until `query_tweaks` and/or engine gaps are addressed; this setup is meant to give a **reproducible** baseline.
