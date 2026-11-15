"""Fake Telegram Bot API for the updater tests: records every call, serves queued updates.

  python3 fake_telegram.py PORT DIR
DIR/updates.json  -- list of Update objects to serve from getUpdates (offset honoured)
DIR/calls.jsonl   -- one {"method":..., "params":{...}} line per call, appended
"""
import json, os, sys, time, urllib.parse
from http.server import BaseHTTPRequestHandler, HTTPServer

port, d = int(sys.argv[1]), sys.argv[2]

class H(BaseHTTPRequestHandler):
    def _handle(self):
        u = urllib.parse.urlparse(self.path)
        method = u.path.rsplit("/", 1)[-1]
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode() if n else ""
        params = {k: v[0] for k, v in urllib.parse.parse_qs(u.query + "&" + body).items()}
        with open(os.path.join(d, "calls.jsonl"), "a") as f:
            f.write(json.dumps({"method": method, "token": u.path.split("/")[1][3:], "params": params}) + "\n")
        if method == "getUpdates":
            try:
                ups = json.load(open(os.path.join(d, "updates.json")))
            except Exception:
                ups = []
            off = int(params.get("offset", 0))
            ups = [x for x in ups if x["update_id"] >= off]
            if not ups:
                time.sleep(min(float(params.get("timeout", 0)), 1.0))
            out = {"ok": True, "result": ups}
        elif method == "sendMessage":
            out = {"ok": True, "result": {"message_id": 42}}
        else:
            out = {"ok": True, "result": True}
        b = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    do_GET = do_POST = _handle
    def log_message(self, *a): pass

HTTPServer(("127.0.0.1", port), H).serve_forever()
