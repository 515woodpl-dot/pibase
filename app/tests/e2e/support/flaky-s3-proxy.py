"""HTTP proxy in front of an S3-compatible server that can be told to fail, to test outages.
Mode file (argv[3]) contains: "ok" | "down" (every request 503) | "no-delete" (bulk deletes 503).
Usage: python3 flaky-s3-proxy.py <listen-port> <upstream-port> <mode-file>
"""
import sys, urllib.request, urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN, UP, MODE = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]

def mode():
    try: return open(MODE).read().strip()
    except OSError: return "ok"

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def any(self):
        n = int(self.headers.get("Content-Length") or 0); body = self.rfile.read(n) if n else None
        md = mode()
        if md == "down" or (md == "no-delete" and self.command == "POST" and "delete" in self.path.split("?", 1)[-1]):
            msg = b"<Error><Code>ServiceUnavailable</Code><Message>simulated outage</Message></Error>"
            self.send_response(503); self.send_header("Content-Type", "application/xml")
            self.send_header("Content-Length", str(len(msg))); self.end_headers(); self.wfile.write(msg); return
        req = urllib.request.Request(f"http://127.0.0.1:{UP}{self.path}", data=body, method=self.command)
        for k, v in self.headers.items():
            if k.lower() not in ("host", "content-length", "connection"): req.add_header(k, v)
        try: resp = urllib.request.urlopen(req); code = resp.status
        except urllib.error.HTTPError as e: resp = e; code = e.code
        data = resp.read()
        self.send_response(code)
        for k, v in resp.headers.items():
            if k.lower() not in ("transfer-encoding", "connection", "content-length", "date", "server"): self.send_header(k, v)
        self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    do_GET = do_PUT = do_POST = do_DELETE = do_HEAD = any

ThreadingHTTPServer(("127.0.0.1", LISTEN), H).serve_forever()
