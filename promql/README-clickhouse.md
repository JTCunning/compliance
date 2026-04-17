# PromQL compliance testing against ClickHouse

This adds a **ClickHouse** target for the PromQL compliance tester (`promql-compliance-tester`), comparing **reference Prometheus** (port **9090**) with ClickHouse’s **Prometheus-compatible query API** on the **host** port (**19093** by default; mapped to **9093** inside the container).

Upstream docs: [promql/README.md](./README.md).

**VM workflow, port conflicts, Docker auth, and log analysis** for agents: use the local Cursor skill **`clickhouse-vm-prometheus-compliance`** (workspace `~/.cursor/skills/clickhouse-vm-prometheus-compliance/SKILL.md` — not part of this Git repository).

## Layout

| Path | Purpose |
|------|---------|
| [test-clickhouse.yml](./test-clickhouse.yml) | `query_url` for reference vs ClickHouse (base URL only; no `/api/v1/query` suffix). |
| [prometheus-test-data-clickhouse.yml](./prometheus-test-data-clickhouse.yml) | Demo scrape targets + `remote_write` to ClickHouse `/write` on the host port. |
| [clickhouse-docker/](./clickhouse-docker/) | `docker compose` stack, XML for `prometheus` HTTP + `TimeSeries` profile. |
| [scripts/run-on-vm-default-ports.sh](./scripts/run-on-vm-default-ports.sh) | One-shot: compose up → `CREATE TABLE` → Prometheus on **9090** → run tester (`test-clickhouse.yml`). |

## Prometheus Go client and POST bodies

`github.com/prometheus/client_golang` **`api/prometheus/v1`** sends **`QueryRange`** / **`Query`** as **`POST`** with **`application/x-www-form-urlencoded`** first (**`DoGetFallback`** retries **GET** only on **405** / **501**). There is no env flag to force **GET**.

ClickHouse **`master`** parses these POST bodies correctly as of commit [`269be3c54ab` — *Parse Prometheus Query API POST bodies as urlencoded form*](https://github.com/ClickHouse/ClickHouse/commit/269be3c54ab) (first reported `clickhouse local` version: **`26.4.1.1`**). Older builds — including `clickhouse/clickhouse-server:latest` until the next stable rolls forward — saw `params->get("query", "")` come back empty in **`QueryAPIImpl`** (`PrometheusRequestHandler.cpp` + **`HTMLForm`**) on the Go client’s POST and answered with `bad_data: mismatched input '<EOF>' … at position 0`. Use a master-based image (see [Source-built image](#source-built-image)) to exercise the fix until that release reaches `latest`.

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

Image default: `clickhouse/clickhouse-server:latest`. **`latest` may trail `master`** and, until the next stable, will not contain the POST-body fix described in [Prometheus Go client and POST bodies](#prometheus-go-client-and-post-bodies); for that fix today, build from source — see [Source-built image](#source-built-image) below.

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

## Expectations

First runs may not be 100% passing until `query_tweaks` and/or engine gaps are addressed; this setup is meant to give a **reproducible** baseline.

On this fork branch, `promql-compliance-tester` **records** internal compare errors (for example when the reference Prometheus version no longer matches a testcase’s `should_fail` expectation) as failed results so the run still prints a **`Total: … passed`** summary instead of exiting early.

### Measured pass rate and what failed (informative)

Two reference runs against the same `prometheus-test-data-clickhouse.yml` and reference Prometheus 2.45.x, expanding to **539** testcase executions, reference Prometheus on `:9090`, ClickHouse on `:19093`:

| Image | `clickhouse local --version` | Warmup | Result |
|-------|------------------------------|--------|--------|
| `clickhouse/clickhouse-server:latest` (pre-POST-fix) | older `latest` | short | **`Total: 4 / 539 (0.74%) passed, 0 unsupported`** |
| `ch-promql:local` from `master` after [`269be3c54ab`](https://github.com/ClickHouse/ClickHouse/commit/269be3c54ab) | **`26.4.1.1`** | ~6 min | **`Total: 275 / 539 (51.02%) passed, 0 unsupported`** |

The +50-percentage-point jump is entirely from the POST-body fix landing on master: the 534 pre-fix `bad_data: mismatched input '<EOF>' … at position 0` failures all became real result comparisons. Once those go away, the remaining failures on the master run are **engine gaps in ClickHouse’s PromQL implementation**, not transport problems:

| Count | Bucket (verbatim from `bad_data:`) |
|------:|------------------------------------|
| 54 | `Function quantile_over_time is not implemented` |
| 12 | `Function histogram_quantile is not implemented` |
| 7 each | `Function max_over_time / avg_over_time / increase / label_replace is not implemented` |
| 6 each | `Function sum_over_time / min_over_time / count_over_time / stddev_over_time / stdvar_over_time / absent_over_time / changes / resets / deriv / predict_linear is not implemented` |
| 4 each | `Function timestamp / clamp is not implemented`, `Aggregation operator 'topk' / 'bottomk' is not implemented` |
| 3 | `Function label_join is not implemented` |
| 2 each | `Function round / absent is not implemented`, `Quantile level is out of range [0..1]` |
| 1 each | `Function clamp_min / clamp_max is not implemented`, `Aggregation operator 'count_values' is not implemented`, `Function 'year' / 'month' / 'hour' / 'minute' / 'day_of_month' / 'day_of_week' / 'days_in_month' expects 1 arguments` |
| 51 | `Query returned different results` (semantic mismatch — usually fewer/more series in this short warmup) |
| 1 | `Query succeeded, but should have failed.` — `label_replace(demo_num_cpus, "instance", "", "", "")`; reference Prometheus 2.45.x accepts what the testcase expected to fail (this fork’s change turns that into a counted failure instead of aborting the run). |

For agents: interpret logs, free ports, manage Docker lifecycle, and reproduce on a Linux VM via that skill. Use a **longer** warmup (upstream suggests ~1 hour) if you want to drive down the 51 `Query returned different results` rows that come from sparse range data.
