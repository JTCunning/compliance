# PromQL compliance tests for ClickHouse

This directory adds a ClickHouse target for `promql-compliance-tester`. The tester compares a reference Prometheus with the Prometheus HTTP API that ClickHouse serves over a `TimeSeries` table.

General usage of the tester is in [README.md](./README.md).

## Files

| Path | Purpose |
|------|---------|
| [test-clickhouse.yml](./test-clickhouse.yml) | Reference and test target URLs for the tester. |
| [clickhouse-docker/docker-compose.yml](./clickhouse-docker/docker-compose.yml) | ClickHouse master build plus a reference Prometheus. |
| [clickhouse-docker/config.d/prometheus-api.xml](./clickhouse-docker/config.d/prometheus-api.xml) | One `prometheus_api_v1` HTTP handler under `/prometheus/api/v1` bound to `default.prometheus`. |
| [clickhouse-docker/users.d/time-series.xml](./clickhouse-docker/users.d/time-series.xml) | Enables `enable_time_series_table` for the default profile. |
| [clickhouse-docker/initdb/create-table.sql](./clickhouse-docker/initdb/create-table.sql) | Creates `default.prometheus` with `ENGINE = TimeSeries` on first start. |
| [clickhouse-docker/prometheus.yml](./clickhouse-docker/prometheus.yml) | Demo scrape targets from `prometheus-test-data.yml` plus `remote_write` to ClickHouse. |

## Components

| Component | Default image | Host port |
|-----------|---------------|-----------|
| ClickHouse | `clickhouse/clickhouse-server:head` (built from the `master` branch) | 8123 |
| Reference Prometheus | `prom/prometheus:v3.15.0` | 9090 |

The handler serves `/prometheus/api/v1/write`, `/prometheus/api/v1/query`, `/prometheus/api/v1/query_range`, and the metadata endpoints. `test-clickhouse.yml` points the tester at `http://localhost:8123/prometheus`. The tester appends `/api/v1/query_range`.

`CLICKHOUSE_SKIP_USER_SETUP=1` keeps the built-in `default` user reachable from the compose network so that Prometheus can remote write. Use this stack on an isolated test host only.

## Run

Prerequisites: Docker with the compose plugin, and Go.

1. Start the stack from `promql/clickhouse-docker/`:

   ```bash
   docker compose up -d
   ```

2. Confirm that samples arrive in ClickHouse:

   ```bash
   curl -s 'http://localhost:8123/prometheus/api/v1/query?query=up'
   ```

3. Wait at least 90 minutes. The test cases use ranges up to 1h and offsets up to 10m. The tester evaluates a 10 minute window. The window ends 12 minutes before the run.

4. Build and run the tester from `promql/`:

   ```bash
   go build ./cmd/promql-compliance-tester
   ./promql-compliance-tester -config-file=promql-test-queries.yml -config-file=test-clickhouse.yml
   ```

5. Stop the stack from `promql/clickhouse-docker/`:

   ```bash
   docker compose down -v
   ```

## Ports and images

The compose file reads these variables:

| Variable | Default |
|----------|---------|
| `CLICKHOUSE_IMAGE` | `clickhouse/clickhouse-server:head` |
| `CLICKHOUSE_HTTP_HOST_PORT` | `8123` |
| `PROMETHEUS_IMAGE` | `prom/prometheus:v3.15.0` |
| `PROMETHEUS_HOST_PORT` | `9090` |

When you change a host port, change the matching `query_url` in `test-clickhouse.yml`.

To test a specific ClickHouse build, set `CLICKHOUSE_IMAGE` to an image built from [docker/server](https://github.com/ClickHouse/ClickHouse/tree/master/docker/server).

## Result

Run date: 2026-09-30.

ClickHouse `SELECT version()` returned `26.10.1.1088`. The image `clickhouse/clickhouse-server:head` reports git hash `8dc18243e09089c778b53095a76a582de723f3a4`.

The reference server was Prometheus `3.15.0`, revision `5241a27fe3c6983549fccc32f6e65917408c63cd`.

This branch also contains the destination-label change from [prometheus/compliance#181](https://github.com/prometheus/compliance/pull/181). Prometheus 3.15.0 accepts the old destination label `~invalid`. The tester then stops, because the reference result does not match `should_fail`.

`test-clickhouse.yml` sets no query tweaks. One run reported:

```text
Total: 534 / 539 (99.07%) passed, 0 unsupported
```

That total is not stable. ClickHouse truncates a query timestamp to the earlier millisecond. Prometheus rounds the same timestamp. When the fractional part of the millisecond is 0.5 or more, every sample timestamp differs by 1ms, and those cases fail.

A second run set `truncate_timestamps_to_ms: 1000` for the comparison only. That setting is not part of `test-clickhouse.yml`. The total was the same:

```text
Total: 534 / 539 (99.07%) passed, 0 unsupported
```

The five failures in that run:

| Query | Difference |
|-------|------------|
| `label_replace(demo_num_cpus, "\xff", "", "src", "(.*)")` | ClickHouse returns a result. Prometheus returns an error. The case expects an error. |
| `label_join(demo_num_cpus, "\xff", "-", "instance")` | ClickHouse returns a result. Prometheus returns an error. The case expects an error. |
| `topk without(instance) (2, demo_memory_usage_bytes)` | ClickHouse returns an extra sample with value `173015040` at steps where Prometheus has no sample. |
| `demo_memory_usage_bytes % 1.2345` | One remainder differs by about 1.4e-7. |
| `demo_memory_usage_bytes % (1 * 2 + 4 / 6 - 10)` | One remainder is `7.333333` on Prometheus and `-4.768e-7` on ClickHouse. |
