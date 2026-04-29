#!/usr/bin/env python3
"""Record storage footprint for the Elastic PromQL compliance benchmark.

Captures:
  - Reference Prometheus TSDB directory size on the host (``du -sb``).
  - Elasticsearch logical index store from ``/_cluster/stats`` (indices.store.size_in_bytes).
  - Elasticsearch on-disk data directory inside the container (``du -sb``), when Docker is available.

Appends one JSON object per line to ``--log`` (JSONL). Intended phases:
  ``post_write`` — after scrape + remote_write warmup, immediately before ``promql-compliance-tester``.
  ``post_read``  — after the tester finishes (queries are mostly read-heavy; sizes usually stable).
"""
import argparse
import json
import subprocess
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any, Dict, Optional


def _du_sb(path: str) -> Optional[int]:
    try:
        out = subprocess.check_output(["du", "-sb", path], text=True, timeout=120)
        return int(out.split()[0])
    except (subprocess.CalledProcessError, FileNotFoundError, ValueError, IndexError):
        return None


def _es_cluster_stats(es_url: str) -> Dict[str, Any]:
    url = es_url.rstrip("/") + "/_cluster/stats"
    with urllib.request.urlopen(url, timeout=60) as resp:
        return json.load(resp)


def _docker_du_es_data(container_name: str) -> Optional[int]:
    try:
        out = subprocess.check_output(
            ["docker", "exec", container_name, "du", "-sb", "/usr/share/elasticsearch/data"],
            text=True,
            timeout=120,
        )
        return int(out.split()[0])
    except (subprocess.CalledProcessError, FileNotFoundError, ValueError, IndexError):
        return None


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--phase",
        required=True,
        help="Label for this snapshot, e.g. post_write or post_read",
    )
    ap.add_argument(
        "--prom-tsdb",
        required=True,
        help="Path to reference Prometheus TSDB directory on the host",
    )
    ap.add_argument(
        "--es-url",
        default="http://127.0.0.1:9200",
        help="Elasticsearch HTTP base URL",
    )
    ap.add_argument(
        "--log",
        default="/tmp/prom_es_benchmark_storage.jsonl",
        help="Append one JSON line per run to this file",
    )
    ap.add_argument(
        "--docker-es-container",
        default="elasticsearch-promql-compliance",
        help="Docker container name for ``docker exec`` data-dir size (skip if unset)",
    )
    args = ap.parse_args()

    rec: Dict[str, Any] = {
        "phase": args.phase,
        "ts_utc": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "prometheus_tsdb_path": args.prom_tsdb,
        "prometheus_tsdb_bytes_host_du": _du_sb(args.prom_tsdb),
    }

    try:
        cs = _es_cluster_stats(args.es_url)
        idx = cs.get("indices") or {}
        store = idx.get("store") or {}
        docs = idx.get("docs") or {}
        rec["elasticsearch_indices_store_bytes"] = store.get("size_in_bytes")
        rec["elasticsearch_index_docs_count"] = docs.get("count")
    except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError, TimeoutError) as e:
        rec["elasticsearch_indices_store_bytes"] = None
        rec["elasticsearch_index_docs_count"] = None
        rec["elasticsearch_cluster_stats_error"] = str(e)

    if args.docker_es_container:
        rec["elasticsearch_data_dir_bytes_container_du"] = _docker_du_es_data(
            args.docker_es_container
        )
    else:
        rec["elasticsearch_data_dir_bytes_container_du"] = None

    line = json.dumps(rec, separators=(",", ":"))
    with open(args.log, "a", encoding="utf-8") as f:
        f.write(line + "\n")
    print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
