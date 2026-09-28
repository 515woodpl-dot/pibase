#!/usr/bin/env python3
"""Test stand-in for an email provider and an alert webhook.
  mail-sink.py <smtp_port> <http_port>
SMTP on <smtp_port> accepts every message (optional AUTH). HTTP on <http_port>:
  GET  /messages        → JSON list of received emails {to, from, subject, text, html}
  POST /webhook         → stores the JSON body; GET /webhooks lists them
  POST /reset           → forget everything
Requires: pip install aiosmtpd
"""
import json, sys, threading
from email import message_from_bytes, policy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from aiosmtpd.controller import Controller
from aiosmtpd.smtp import AuthResult

messages, hooks, lock = [], [], threading.Lock()

class Handler:
    async def handle_DATA(self, server, session, envelope):
        m = message_from_bytes(envelope.content, policy=policy.default)
        def part(kind):
            p = m.get_body(preferencelist=(kind,))
            return p.get_content() if p else ""
        with lock:
            messages.append({"to": envelope.rcpt_tos, "from": str(m["From"]), "subject": str(m["Subject"]),
                             "text": part("plain"), "html": part("html")})
        return "250 OK"

def auth_any(server, session, envelope, mechanism, auth_data):
    return AuthResult(success=True)

class Web(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def send(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        with lock:
            if self.path == "/messages": return self.send(messages)
            if self.path == "/webhooks": return self.send(hooks)
        self.send({"error": "not found"}, 404)
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length") or 0))
        with lock:
            if self.path == "/webhook":
                hooks.append(json.loads(body or b"null")); return self.send({"ok": True})
            if self.path == "/reset":
                messages.clear(); hooks.clear(); return self.send({"ok": True})
        self.send({"error": "not found"}, 404)

smtp_port, http_port = int(sys.argv[1]), int(sys.argv[2])
Controller(Handler(), hostname="127.0.0.1", port=smtp_port, authenticator=auth_any, auth_require_tls=False).start()
print(f"mail sink: smtp {smtp_port}, http {http_port}", flush=True)
ThreadingHTTPServer(("127.0.0.1", http_port), Web).serve_forever()
