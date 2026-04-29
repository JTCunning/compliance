#!/usr/bin/env python3
"""Summarize promql-compliance-tester -output-format json for per-query ref vs test review.

The tester already runs each query against **reference_target_config** and **test_target_config**
and records cmp.Diff text when matrix results differ. This script prints counts and lists
failure buckets so you can verify (e.g.) Elasticsearch/shim behaviour vs Prometheus.

Usage:
  ./promql-compliance-tester -output-format json ... > results.json
  python3 scripts/summarize_promql_compliance_json.py results.json
  python3 scripts/summarize_promql_compliance_json.py results.json --queries 5
"""
from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from typing import Any, Dict, List


def success(r: Dict[str, Any]) -> bool:
    return (
        not r.get("diff")
        and not r.get("unexpectedSuccess")
        and not (r.get("unexpectedFailure") or "")
    )


def bucket(r: Dict[str, Any]) -> str:
    if success(r):
        return "passed"
    if r.get("unsupported"):
        return "unsupported"
    if r.get("unexpectedSuccess"):
        return "unexpected_success"
    if r.get("unexpectedFailure"):
        msg = r["unexpectedFailure"]
        if "501" in msg or "Not Implemented" in msg:
            return "unsupported_http"
        if "error querying reference API" in msg:
            return "reference_error"
        if "Elasticsearch returned HTTP" in msg:
            return "test_backend_error"
        return "unexpected_failure"
    if r.get("diff"):
        return "matrix_mismatch"
    return "other"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("json_file", help="Path to JSON written by -output-format json")
    ap.add_argument(
        "--queries",
        type=int,
        default=12,
        help="Max example queries to print per failure bucket (default 12)",
    )
    args = ap.parse_args()

    with open(args.json_file, "r", encoding="utf-8") as f:
        doc = json.load(f)

    results: List[Dict[str, Any]] = doc.get("results") or []
    counts = Counter(bucket(r) for r in results)
    passed = counts.get("passed", 0)

    print(f"totalResults (payload): {doc.get('totalResults', len(results))}")
    print(f"parsed results:        {len(results)}")
    print(f"passed:                {passed}")
    unsupported = counts.get("unsupported", 0) + counts.get("unsupported_http", 0)
    print(
        f"Total (text-style line): {passed} / {len(results)} "
        f"({100 * passed / max(len(results), 1):.2f}%) passed, {unsupported} unsupported"
    )
    print("by outcome:")
    for k in sorted(counts, key=lambda x: (-counts[x], x)):
        print(f"  {k:22s} {counts[k]}")

    print("\n--- Example queries per non-pass bucket ---\n")
    for b in sorted(set(counts) - {"passed"}):
        ex = [r for r in results if bucket(r) == b][: args.queries]
        if not ex:
            continue
        print(f"## {b} ({counts[b]} total, showing up to {args.queries})")
        for r in ex:
            tc = r.get("testCase") or {}
            q = tc.get("query", "?")
            q_short = q if len(q) <= 100 else q[:97] + "..."
            print(f"  - {q_short}")
            if r.get("unexpectedFailure"):
                msg = r["unexpectedFailure"]
                print(f"      err: {msg[:240]}{'...' if len(msg) > 240 else ''}")
            if r.get("diff"):
                d = r["diff"]
                first = d.splitlines()[:8]
                print("      diff (first lines):")
                for ln in first:
                    print(f"        {ln}")
                if len(d.splitlines()) > 8:
                    print("        ...")
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
