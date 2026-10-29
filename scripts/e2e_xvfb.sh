#!/usr/bin/env bash
# e2e headless harness (local dev tool, not committed).
# Launches movian-go under Xvfb with an isolated HOME profile, then
# probes the STPP websocket handshake and the /api/screenshot endpoint.
#
# Usage: ./scripts/e2e_xvfb.sh [binary] [port]
# Requires: xvfb, go, python3.

set -u
BIN="${1:-./movian-go}"
PORT="${2:-42000}"
PROFILE="$(mktemp -d)"
PID=""

cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$PROFILE"; }
trap cleanup EXIT

echo "[e2e] profile: $PROFILE  port: $PORT"
HOME="$PROFILE" xvfb-run -a "$BIN" >"$PROFILE/app.log" 2>&1 &
PID=$!

echo "[e2e] waiting for http on :$PORT ..."
for i in $(seq 1 60); do
  python3 -c "import socket;socket.create_connection(('127.0.0.1',$PORT),1)" 2>/dev/null && break
  sleep 0.5
  [ "$i" = 60 ] && { echo "[e2e] FAIL: no http after 30s"; tail -20 "$PROFILE/app.log"; exit 1; }
done
echo "[e2e] http up"

echo "[e2e] STPP handshake probe"
python3 - "$PORT" <<'PY'
import socket,sys,base64,os,hashlib
p=int(sys.argv[1])
key=base64.b64encode(os.urandom(16)).decode()
s=socket.create_connection(("127.0.0.1",p),3)
s.sendall((f"GET /api/stpp HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n"
           f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
           f"Sec-WebSocket-Version: 13\r\n\r\n").encode())
resp=b""
while b"\r\n\r\n" not in resp: resp+=s.recv(4096)
head=resp.split(b"\r\n\r\n")[0].decode()
assert "101" in head.split("\r\n")[0], head
conns=[l for l in head.split("\r\n") if l.lower().startswith("connection:")]
assert len(conns)==1 and "upgrade" in conns[0].lower(), conns
assert "content-length" not in head.lower(), head
exp=base64.b64encode(hashlib.sha1((key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
assert f"Sec-WebSocket-Accept: {exp}" in head, head
# socket must stay open — no immediate EOF
s.settimeout(0.5)
try:
    b=s.recv(1)
    if b==b"": raise SystemExit("[e2e] FAIL: socket closed after 101")
except socket.timeout: pass
print("[e2e] STPP handshake OK (101, single Connection, no Content-Length, open)")
PY

echo "[e2e] screenshot probe (retry until first GL frame)"
SHOT=""
for i in $(seq 1 20); do
  curl -sf "http://127.0.0.1:$PORT/api/screenshot" -o "$PROFILE/shot.png" 2>/dev/null
  [ -s "$PROFILE/shot.png" ] && SHOT=yes && break
  sleep 0.5
done
if [ -n "$SHOT" ]; then
  echo "[e2e] screenshot OK ($(wc -c <"$PROFILE/shot.png") bytes)"
else
  echo "[e2e] FAIL: screenshot stayed empty"
  exit 1
fi

echo "[e2e] done"
