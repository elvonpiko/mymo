#!/usr/bin/env bash
# Records screenshots/demo.gif from the real mymo binary in a real
# terminal (vhs). A local SSH peer stands in for one node so opening
# it is a real connection and the gauges move.
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v vhs >/dev/null; then
  echo "record-demo: vhs is not installed" >&2
  exit 1
fi

tmp="$(mktemp -d)"
box=""
trap '[[ -n "$box" ]] && kill "$box" 2>/dev/null || true; rm -rf "$tmp"; rm -f ./mymo' EXIT

go build -o "$tmp/mymo" ./cmd/mymo
mkdir -p "$tmp/home/.mymo/keys"
chmod 700 "$tmp/home/.mymo" "$tmp/home/.mymo/keys"
ssh-keygen -q -t ed25519 -N "" -f "$tmp/home/.mymo/keys/chicago.key"

go run scripts/demobox.go -addr 127.0.0.1:8022 >"$tmp/demobox.addr" 2>"$tmp/demobox.err" &
box=$!
for _ in $(seq 1 80); do
  if grep -q . "$tmp/demobox.addr" 2>/dev/null; then
    break
  fi
  sleep 0.25
done
if ! grep -q . "$tmp/demobox.addr"; then
  echo "record-demo: demobox did not start" >&2
  cat "$tmp/demobox.err" >&2 || true
  exit 1
fi

python3 - "$tmp/home/.mymo" "$(cat "$tmp/demobox.addr")" <<'PY'
import json, sys, datetime
from pathlib import Path
root = Path(sys.argv[1])
host, port_s = sys.argv[2].rsplit(":", 1)
port = int(port_s)
now = datetime.datetime.now(datetime.timezone.utc)
def t(delta):
    return (now - datetime.timedelta(seconds=delta)).strftime("%Y-%m-%dT%H:%M:%SZ")
key = str(root / "keys" / "chicago.key")
nodes = [
  {
    "name": "chicago", "host": host, "port": port,
    "user": "mymo", "auth": "key", "key_path": key, "mode": "app-host",
    "added_at": t(60*86400),
    "facts": {"hostname": "chicago", "os": "Ubuntu 24.04.2 LTS", "kernel": "6.8.0-45-generic",
              "arch": "x86_64", "cpus": 2, "mem_total": 2048000*1024, "systemd": True, "user": "mymo"},
    "last_check": {"at": t(120)},
    "bootstrap": {"state": "ready", "baseline": "0.1", "verdict": "ready", "at": t(50*86400)},
  },
  {
    "name": "helsinki", "host": "10.2.0.12", "port": 22,
    "user": "root", "auth": "agent", "mode": "app-host",
    "added_at": t(40*86400),
    "facts": {"hostname": "helsinki", "os": "Ubuntu 24.04.1 LTS", "cpus": 4, "mem_total": 8<<30},
    "last_check": {"at": t(6*3600)},
    "bootstrap": {"state": "ready", "baseline": "0.1", "verdict": "ready", "at": t(30*86400)},
  },
  {
    "name": "frankfurt", "host": "10.2.0.20", "port": 22,
    "user": "root", "auth": "agent", "mode": "observe",
    "added_at": t(20*86400),
    "facts": {"hostname": "frankfurt", "os": "Debian GNU/Linux 12 (bookworm)", "cpus": 2, "mem_total": 2<<30},
    "last_check": {"at": t(30*3600)},
  },
  {
    "name": "osaka", "host": "10.2.0.31", "port": 22,
    "user": "root", "auth": "agent", "mode": "observe",
    "added_at": t(9*86400),
    "facts": {"hostname": "osaka", "os": "Debian GNU/Linux 13 (trixie)", "cpus": 2},
    "last_check": {"at": t(3*3600), "error": "unreachable: connection refused"},
  },
]
apps = [{
  "name": "web", "node": "chicago", "type": "image",
  "image": "ghcr.io/elvonpiko/web:1.4.2", "port": 8080,
  "domain": "web.chicago.dev", "health": "/healthz", "network_ip": "172.20.0.2",
  "added_at": t(20*86400),
  "releases": [
    {"id": 2, "digest": "sha256:e7d94a11", "source": "image ghcr.io/elvonpiko/web:1.4.2",
     "container": "mymo-web-r2", "health": "/healthz answers", "state": "active",
     "deployed_at": t(3*86400)},
  ],
}]
def write(name, doc):
    p = root / name
    p.write_text(json.dumps(doc, indent=2) + "\n")
    p.chmod(0o600)
write("nodes.json", {"schema_version": 1, "nodes": nodes})
write("apps.json", {"schema_version": 1, "apps": apps})
write("meta.json", {"schema_version": 1, "intro_seen": True})
PY

cp "$tmp/mymo" ./mymo
chmod +x ./mymo
# The agent environment sets NO_COLOR, which strips every color
# mymo draws. A recording made that way is one gray. Drop it.
env -u NO_COLOR \
  HOME="$tmp/home" USERPROFILE="$tmp/home" \
  COLORTERM=truecolor CLICOLOR_FORCE=1 TERM=xterm-256color \
  vhs scripts/demo.tape

# GIF holds 256 colors. A free quantization turns mauve, teal, and
# green into one gray. Pin the palette to the colors mymo actually
# draws, then fill the rest from the recording.
python3 - << 'PY'
from PIL import Image
colors = [
    (0x1e,0x1e,0x2e),(0x31,0x32,0x44),(0x45,0x47,0x5a),(0x58,0x5b,0x70),
    (0x6c,0x70,0x86),(0x7f,0x84,0x9c),(0x93,0x99,0xb2),(0xa6,0xad,0xc8),
    (0xba,0xc2,0xde),(0xcd,0xd6,0xf4),(0xcb,0xa6,0xf7),(0xb4,0xbe,0xfe),
    (0x89,0xdc,0xeb),(0x94,0xe2,0xd5),(0xfa,0xb3,0x87),(0xa6,0xe3,0xa1),
    (0xf9,0xe2,0xaf),(0xf3,0x8b,0xa8),(0xf5,0xc2,0xe7),(0x11,0x11,0x1b),
    (0x18,0x18,0x25),(0x24,0x24,0x36),(0x3b,0x3d,0x52),(0xff,0xff,0xff),
    (0x00,0x00,0x00),
]
# paletteuse reads an image whose pixels are the colors, one each,
# the same shape palettegen writes.
img = Image.new("RGB", (16, 16), (0, 0, 0))
px = img.load()
for i, c in enumerate(colors):
    px[i % 16, i // 16] = c
img.save("/tmp/mymo-pinned.png")
PY

ffmpeg -y -i screenshots/demo.mp4 -update 1 -frames:v 1 -vf "fps=16,scale=1080:-1:flags=lanczos,palettegen=max_colors=232:stats_mode=diff:reserve_transparent=0" /tmp/mymo-gen.png
# splice the pinned colors into the generated palette's first slots
python3 - << 'PY'
from PIL import Image
gen = Image.open("/tmp/mymo-gen.png").convert("RGB")
pinned = Image.open("/tmp/mymo-pinned.png").convert("RGB")
def colors_of(im):
    seen = []
    for c in im.getdata():
        if c not in seen:
            seen.append(c)
    return seen
def near(a, b):
    return abs(a[0]-b[0])+abs(a[1]-b[1])+abs(a[2]-b[2]) < 24
pins = [c for c in colors_of(pinned) if c != (0, 0, 0)]
pins.append((0, 0, 0))
rest = [c for c in colors_of(gen) if not any(near(c, p) for p in pins)]
out = (pins + rest)[:256]
img = Image.new("RGB", (16, 16), (0, 0, 0))
px = img.load()
for i, c in enumerate(out):
    px[i % 16, i // 16] = c
img.save("/tmp/mymo-palette.png")
print("palette", len(out), "pinned", pins[:8])
PY

ffmpeg -y -i screenshots/demo.mp4 -i /tmp/mymo-palette.png -lavfi "fps=16,scale=1080:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle" -loop 0 screenshots/demo.gif
rm -f screenshots/demo.mp4
echo "recorded screenshots/demo.gif"
