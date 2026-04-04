#!/usr/bin/env python3
"""
Translate Prometheus HTTP API POSTs (application/x-www-form-urlencoded) into GETs.

prometheus/client_golang v1 always POSTs first in DoGetFallback; ClickHouse's query
handler may only read query parameters from GET. This proxy sits in front of ClickHouse.

Environment:
  PROMQL_GET_PROXY_UPSTREAM   default http://127.0.0.1:19093
  PROMQL_GET_PROXY_LISTEN     default 0.0.0.0:29193

No changes to promql-compliance-tester are required; point test_target_config.query_url
at http://localhost:<listen_port> (see test-clickhouse-get-proxy.yml).
"""
from __future__ import annotations

import os
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qsl, urlencode, urlparse

UPSTREAM = os.environ.get("PROMQL_GET_PROXY_UPSTREAM", "http://127.0.0.1:19093").rstrip("/")
LISTEN = os.environ.get("PROMQL_GET_PROXY_LISTEN", "0.0.0.0:29193")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args) -> None:
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def _forward_request(self, method: str, path: str, body: bytes | None) -> None:
        url = UPSTREAM + path
        req = urllib.request.Request(url, data=body, method=method)
        # Drop hop-by-hop headers from client
        skip = {
            "host",
            "connection",
            "proxy-connection",
            "keep-alive",
            "te",
            "trailer",
            "transfer-encoding",
            "upgrade",
            "content-length",
        }
        for h, v in self.headers.items():
            if h.lower() in skip:
                continue
            req.add_header(h, v)
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = resp.read()
                self.send_response(resp.status)
                for hk, hv in resp.headers.items():
                    kl = hk.lower()
                    if kl in ("transfer-encoding", "connection"):
                        continue
                    self.send_header(hk, hv)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        except urllib.error.HTTPError as e:
            data = e.read() if e.fp else b""
            self.send_response(e.code)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except Exception as e:
            msg = str(e).encode("utf-8")
            self.send_response(502)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)

    def do_GET(self) -> None:
        self._forward_request("GET", self.path, None)

    def do_POST(self) -> None:
        parsed = urlparse(self.path)
        path = parsed.path or "/"
        if path not in ("/api/v1/query", "/api/v1/query_range"):
            self._forward_request("POST", self.path, self._read_body())
            return
        raw = self._read_body().decode("utf-8", errors="replace")
        pairs = parse_qsl(raw, keep_blank_values=True)
        qs = urlencode(pairs)
        new_path = path + ("?" + qs if qs else "")
        self._forward_request("GET", new_path, None)

    def _read_body(self) -> bytes:
        n = self.headers.get("Content-Length")
        if not n:
            return b""
        return self.rfile.read(int(n))


def main() -> None:
    host, _, port = LISTEN.rpartition(":")
    if not port:
        host, port = "0.0.0.0", LISTEN
    httpd = ThreadingHTTPServer((host, int(port)), Handler)
    sys.stderr.write(
        "prometheus POST→GET proxy listening %s → upstream %s\n" % (LISTEN, UPSTREAM)
    )
    httpd.serve_forever()


if __name__ == "__main__":
    main()
