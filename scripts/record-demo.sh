#!/usr/bin/env bash
# Records screenshots/demo.gif from mymo running in Ghostty.
#
# VHS redraws the program in its own font and theme, then compresses
# that through a video codec, so the picture never matches the
# terminal. This opens the real Ghostty (the same font, hinting, and
# background as a normal window), types into that window, and grabs
# the screen pixels straight into a GIF.
#
# Needs, on this machine: ghostty, ffmpeg, i3, xrandr, and python3-xlib.
# A local SSH peer stands in for one node so opening it is a real
# connection and the gauges move. Nothing is read from or written to
# the real ~/.mymo — Ghostty resets HOME, so the child is started
# from a wrapper that points HOME at the throwaway directory, and the
# script refuses to continue if that did not take.
set -euo pipefail
cd "$(dirname "$0")/.."

for cmd in ghostty ffmpeg i3-msg xwininfo xrandr ss pgrep python3 go ssh-keygen; do
  if ! command -v "$cmd" >/dev/null; then
    echo "record-demo: $cmd is not installed" >&2
    exit 1
  fi
done
python3 -c 'from Xlib import display' 2>/dev/null || {
  echo "record-demo: python3-xlib is not installed" >&2
  exit 1
}

tmp="$(mktemp -d)"
box=""
gp=""
ff=""
cleanup() {
  [[ -n "$ff" ]] && kill -INT "$ff" 2>/dev/null || true
  [[ -n "$gp" ]] && kill "$gp" 2>/dev/null || true
  [[ -n "$box" ]] && kill "$box" 2>/dev/null || true
  if [[ -f "$tmp/focus" ]]; then
    python3 -c '
import sys
from Xlib import X, display
from Xlib.error import XError
fid_s, rev_s = open(sys.argv[1]).read().split()
fid, rev = int(fid_s), int(rev_s)
d = display.Display()
try:
    target = fid if fid <= 1 else d.create_resource_object("window", fid)
    d.set_input_focus(target, rev, X.CurrentTime)
    d.sync()
except XError:
    d.set_input_focus(X.PointerRoot, X.RevertToPointerRoot, X.CurrentTime)
    d.sync()
' "$tmp/focus" || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

python3 - "$tmp/focus" <<'PY'
import sys
from Xlib import display
d = display.Display()
f = d.get_input_focus()
focus = f.focus
fid = focus if isinstance(focus, int) else focus.id
open(sys.argv[1], "w").write(f"{fid} {f.revert_to}\n")
PY

go build -o "$tmp/mymo" ./cmd/mymo
go build -o "$tmp/demobox" scripts/demobox.go
mkdir -p "$tmp/home/.mymo/keys"
chmod 700 "$tmp/home/.mymo" "$tmp/home/.mymo/keys"
ssh-keygen -q -t ed25519 -N "" -f "$tmp/home/.mymo/keys/chicago.key"

if ss -ltn | grep -q '127.0.0.1:8022'; then
  echo "record-demo: 127.0.0.1:8022 is already in use" >&2
  exit 1
fi
"$tmp/demobox" -addr 127.0.0.1:8022 >"$tmp/demobox.addr" 2>"$tmp/demobox.err" &
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

# Ghostty replaces HOME with the login home before exec. The wrapper
# puts it back so the recording cannot touch the real ~/.mymo.
cat >"$tmp/run" <<EOF
#!/bin/sh
export HOME='$tmp/home'
export USERPROFILE='$tmp/home'
unset NO_COLOR
export COLORTERM=truecolor
exec '$tmp/mymo'
EOF
chmod +x "$tmp/run"

env -u NO_COLOR \
  ghostty --title='mymo-demo' \
    --gtk-titlebar=false --window-decoration=false \
    --window-save-state=never --confirm-close-surface=false \
    -e "$tmp/run" >"$tmp/ghostty.log" 2>&1 &
gp=$!

for _ in $(seq 1 50); do
  if xwininfo -name 'mymo-demo' >"$tmp/xwin.txt" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
if ! grep -q 'Window id:' "$tmp/xwin.txt" 2>/dev/null; then
  echo "record-demo: Ghostty window did not appear" >&2
  cat "$tmp/ghostty.log" >&2 || true
  exit 1
fi

python3 - "$tmp" <<'PY'
import json, subprocess, sys
tmp = sys.argv[1]
tree = json.loads(subprocess.check_output(["i3-msg", "-t", "get_tree"]))
found = None
def walk(n):
    global found
    props = n.get("window_properties") or {}
    if props.get("title") == "mymo-demo" and n.get("window"):
        found = n
        return
    for c in n.get("nodes", []) + n.get("floating_nodes", []):
        walk(c)
walk(tree)
if not found:
    raise SystemExit("record-demo: mymo-demo is not in the i3 tree")
open(f"{tmp}/con", "w").write(str(found["id"]))
open(f"{tmp}/xid", "w").write(str(found["window"]))
PY

# A wide, short window: the home block stays centered with room
# around it, and the fleet and node pages still fit. Centered on
# the screen so a tiled layout cannot squash it.
read -r sw sh < <(xrandr --current | awk '/current/ {for (i=1;i<=NF;i++) if ($i=="current") {print $(i+1), $(i+3); exit}}' | tr -d ',')
ww=1500
wh=820
wx=$(( (sw - ww) / 2 ))
wy=$(( (sh - wh) / 2 ))
if (( wx < 0 )); then wx=0; fi
if (( wy < 0 )); then wy=0; fi
con=$(cat "$tmp/con")
i3-msg "[con_id=\"$con\"] floating enable" >/dev/null
i3-msg "[con_id=\"$con\"] border none" >/dev/null
i3-msg "[con_id=\"$con\"] resize set ${ww} ${wh}" >/dev/null
i3-msg "[con_id=\"$con\"] move position ${wx} ${wy}" >/dev/null
i3-msg "[con_id=\"$con\"] focus" >/dev/null
sleep 0.45

# The child must be the throwaway home, never the login one.
mymo_pid=""
for _ in $(seq 1 30); do
  if mymo_pid=$(pgrep -P "$gp" -f "$tmp/mymo" | head -1) && [[ -n "$mymo_pid" ]]; then
    break
  fi
  sleep 0.1
done
if [[ -z "$mymo_pid" ]]; then
  echo "record-demo: mymo did not start under Ghostty" >&2
  cat "$tmp/ghostty.log" >&2 || true
  exit 1
fi
got_home=$(tr '\0' '\n' <"/proc/$mymo_pid/environ" | sed -n 's/^HOME=//p')
if [[ "$got_home" != "$tmp/home" ]]; then
  echo "record-demo: mymo HOME is '$got_home', not the throwaway directory" >&2
  exit 1
fi

xwininfo -name 'mymo-demo' >"$tmp/xwin.txt"
python3 - "$tmp/xwin.txt" "$tmp/geom" <<'PY'
import re, sys
t = open(sys.argv[1]).read()
def g(k):
    m = re.search(k + r":\s+(-?\d+)", t)
    if not m:
        raise SystemExit(f"record-demo: no {k} in xwininfo")
    return int(m.group(1))
x, y, w, h = g("Absolute upper-left X"), g("Absolute upper-left Y"), g("Width"), g("Height")
w -= w % 2
h -= h % 2
open(sys.argv[2], "w").write(f"{x} {y} {w} {h}\n")
print(f"record-demo: capturing {w}x{h} at {x},{y}")
PY
read -r x y w h <"$tmp/geom"

ffmpeg -y -loglevel error -f x11grab -draw_mouse 0 -framerate 16 \
  -video_size "${w}x${h}" -i ":0.0+${x},${y}" \
  -c:v ffv1 "$tmp/cap.mkv" >"$tmp/ffmpeg.log" 2>&1 &
ff=$!
sleep 0.15

# Home is already on screen (the typewriter is a few seconds). Hold
# it, open the fleet, walk the cursor back to chicago, let the live
# gauges move, open its app, then return home.
python3 - "$tmp/xid" "$con" <<'PY'
import subprocess, sys, time
from Xlib import X, XK, display
from Xlib.ext.xtest import fake_input

wid = int(open(sys.argv[1]).read().strip())
con = sys.argv[2]
d = display.Display()
win = d.create_resource_object("window", wid)

def focused_id():
    cur = d.get_input_focus().focus
    return cur if isinstance(cur, int) else cur.id

def focus():
    subprocess.check_call(["i3-msg", f'[con_id="{con}"] focus'], stdout=subprocess.DEVNULL)
    d.set_input_focus(win, X.RevertToPointerRoot, X.CurrentTime)
    d.sync()
    time.sleep(0.04)
    if focused_id() != wid:
        raise SystemExit(f"record-demo: focus is {focused_id():#x}, want {wid:#x}")

def tap(name):
    focus()
    code = d.keysym_to_keycode(XK.string_to_keysym(name))
    if not code:
        raise SystemExit(f"record-demo: no keycode for {name}")
    fake_input(d, X.KeyPress, code)
    d.sync()
    time.sleep(0.03)
    fake_input(d, X.KeyRelease, code)
    d.sync()

def pause(s):
    time.sleep(s)

pause(4.0)          # the sentence finishes, then the home page sits
tap("n")
pause(1.8)          # fleet
tap("Down"); pause(0.45)
tap("Down"); pause(0.45)
tap("Up"); pause(0.40)
tap("Up"); pause(0.9)
tap("Return")
pause(7.2)          # dial, then a few live samples
tap("a")
pause(2.2)          # the app on chicago
tap("Escape")
pause(1.0)
tap("h")
pause(4.2)          # home again, sentence finishes
PY

kill -INT "$ff" 2>/dev/null || true
wait "$ff" || true
ff=""
kill "$gp" 2>/dev/null || true
wait "$gp" || true
gp=""

# No dither: a flat background must stay one color. The brand colors
# are pinned so quantization cannot fold mauve, teal, and green into gray.
ffmpeg -y -loglevel error -i "$tmp/cap.mkv" -vf "fps=16,palettegen=max_colors=248:stats_mode=diff:reserve_transparent=0" "$tmp/gen.png"
python3 - "$tmp/gen.png" "$tmp/pal.png" <<'PY'
from PIL import Image
import sys
gen = Image.open(sys.argv[1]).convert("RGB")
pins = [
    (0x28,0x2c,0x34),  # Ghostty default background
    (0xcb,0xa6,0xf7),  # mauve
    (0xb4,0xbe,0xfe),  # lavender
    (0x94,0xe2,0xd5),  # teal
    (0xa6,0xe3,0xa1),  # green
    (0xcd,0xd6,0xf4),  # text
    (0xa6,0xad,0xc8),  # subtext
    (0x6c,0x70,0x86),  # faint
    (0x31,0x32,0x44),  # surface
    (0xf9,0xe2,0xaf),  # yellow
    (0xf3,0x8b,0xa8),  # red
    (0xfa,0xb3,0x87),  # peach
    (0x89,0xdc,0xeb),  # sky
    (0xff,0xff,0xff),
    (0x00,0x00,0x00),
]
def near(a, b):
    return abs(a[0]-b[0]) + abs(a[1]-b[1]) + abs(a[2]-b[2]) < 18
seen = []
for c in gen.get_flattened_data():
    if c not in seen:
        seen.append(c)
rest = [c for c in seen if not any(near(c, p) for p in pins)]
out = (pins + rest)[:256]
img = Image.new("RGB", (16, 16), pins[0])
px = img.load()
for i, c in enumerate(out):
    px[i % 16, i // 16] = c
img.save(sys.argv[2])
print("palette", len(out))
PY
mkdir -p screenshots
ffmpeg -y -loglevel error -i "$tmp/cap.mkv" -i "$tmp/pal.png" \
  -lavfi "fps=16[x];[x][1:v]paletteuse=dither=none:diff_mode=rectangle" \
  -loop 0 "$tmp/demo.gif"

# A black or gray grab means the screen grab missed the window.
python3 - "$tmp/demo.gif" <<'PY'
import sys
from PIL import Image
im = Image.open(sys.argv[1])
im.seek(im.n_frames // 5)
im = im.convert("RGB")
px = im.load()
w, h = im.size
mauve = bg = 0
for y in range(0, h, 3):
    for x in range(0, w, 3):
        r, g, b = px[x, y]
        if abs(r-40) < 8 and abs(g-44) < 8 and abs(b-52) < 8:
            bg += 1
        if abs(r-203) < 18 and abs(g-166) < 24 and abs(b-247) < 18:
            mauve += 1
print(f"record-demo: frame {im.size} bg={bg} mauve={mauve} frames={Image.open(sys.argv[1]).n_frames}")
if bg < 1000 or mauve < 20:
    raise SystemExit("record-demo: captured frame does not look like the Ghostty window")
PY

mv "$tmp/demo.gif" screenshots/demo.gif
echo "recorded screenshots/demo.gif"
