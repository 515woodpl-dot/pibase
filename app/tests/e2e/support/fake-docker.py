"""Minimal fake of the Docker Engine API, enough for DATBase's provisioning/recovery code.
Real PocketBase processes stand in for containers; this only records what DATBase asks Docker to do.
Control:  GET /_log  (JSON list of calls)   POST /_forget (all containers vanish, e.g. host lost)
          POST /_image (a new project image was built: new image id)
Usage:    python3 fake-docker.py 2375       then run the app with DOCKER_HOST=tcp://127.0.0.1:2375
"""
import json, re, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

containers, calls, image = {}, [], {"gen": 1}
image_id = lambda: f"sha256:fake-image-{image['gen']}"

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def _send(self, code, body=None, ctype="application/json"):
        data = b"" if body is None else (body if isinstance(body, bytes) else json.dumps(body).encode())
        self.send_response(code); self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""
    def log_message(self, *a): pass
    def handle_any(self):
        path = re.sub(r"^/v[\d.]+", "", self.path.split("?")[0]); body = self._body()
        m = self.command
        if path == "/_log": return self._send(200, calls)
        if path == "/_forget": containers.clear(); return self._send(200, {"ok": True})
        if path == "/_image": image["gen"] += 1; return self._send(200, {"Id": image_id()})
        if m == "GET" and re.match(r"^/images/.+/json$", path): return self._send(200, {"Id": image_id()})
        calls.append(f"{m} {path}")
        if m == "POST" and path == "/volumes/create": return self._send(201, {"Name": json.loads(body or b"{}").get("Name")})
        if m == "POST" and path == "/containers/create":
            name = re.search(r"name=([^&]+)", self.path).group(1); containers[name] = {"state": "created", "image": image_id()}
            return self._send(201, {"Id": name, "Warnings": []})
        mm = re.match(r"^/containers/([^/]+)(/.*)?$", path)
        if mm:
            name, rest = mm.group(1), mm.group(2) or ""
            if m == "GET" and rest == "/json":
                return self._send(200, {"Id": name, "Image": containers[name]["image"], "State": {"Running": containers[name]["state"] == "running"}}) if name in containers else self._send(404, {"message": "No such container"})
            if m == "DELETE" and rest == "": containers.pop(name, None); return self._send(204)
            if name not in containers: return self._send(404, {"message": "No such container"})
            if rest == "/start": containers[name]["state"] = "running"; return self._send(204)
            if rest == "/stop": containers[name]["state"] = "stopped"; return self._send(204)
            if rest == "/exec": return self._send(201, {"Id": "exec-" + name})
        if m == "POST" and re.match(r"^/exec/[^/]+/start$", path): return self._send(200, b"", "application/vnd.docker.raw-stream")
        if m == "GET" and re.match(r"^/exec/[^/]+/json$", path): return self._send(200, {"ExitCode": 0, "Running": False})
        if m == "DELETE" and path.startswith("/volumes/"): return self._send(204)
        return self._send(404, {"message": "fake-docker: unhandled " + path})
    do_GET = do_POST = do_DELETE = do_PUT = handle_any

ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 2375), H).serve_forever()
