"""临时分析器：把 Go goroutine dump 压缩成「状态 + 本项目帧」清单。

用法: python scripts/tmp-goroutine-map.py <dump.txt> [过滤子串]
"""
import re
import sys

path = sys.argv[1]
needle = sys.argv[2] if len(sys.argv) > 2 else "aicli"
text = open(path, "r", encoding="utf-8", errors="replace").read()
blocks = re.split(r"\n\s*\n", text)

interesting = []
states = {}
for b in blocks:
    m = re.match(r"goroutine (\d+) \[([^\]]+)\]", b.strip())
    if not m:
        continue
    gid, state = m.group(1), m.group(2)
    states[state] = states.get(state, 0) + 1
    if needle not in b:
        continue
    frames = []
    for line in b.splitlines()[1:]:
        s = line.strip()
        if not s or s.startswith(("created by", "runtime.", "internal/", "sync.", "net/http", "os.", "bufio.")):
            continue
        # Go dump: "<func name>\t<file>:<line> +0x.."; 用 tab 切分拿完整函数名
        parts = s.split("\t")
        fn = parts[0].strip()
        loc = ""
        if len(parts) > 1:
            lm = re.search(r"(backend[/\\][\w./\\-]+:\d+)", parts[1])
            if lm:
                loc = " @ " + lm.group(1).replace("\\", "/")
        if fn:
            frames.append(fn + loc)
    interesting.append((gid, state, frames))

print(f"goroutines: {len(blocks)}  state histogram: {states}")
print(f"--- goroutines touching '{needle}': {len(interesting)} ---")
for gid, state, frames in interesting:
    print(f"\n#{gid} [{state}]")
    for f in frames[:14]:
        print("   ", f)
