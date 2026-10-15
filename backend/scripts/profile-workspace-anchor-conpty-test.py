"""ConPTY E2E regression: `/profile use` must not move the session workspace.

Regression under test (fixed in b5377b1f, 2026-10-15):
  Before the fix, `resolveLocalWorkspacePath` anchored the workspace to
  `session.ProfileRoot`, and `syncChatRuntimeContext` recomputed/overwrote
  `sessionmeta.WorkspacePath` on every sync. After `/profile use <name>` the
  session workspace silently drifted to the profile directory
  (`<home>/.aicli/profiles/<name>`) — visible in the bottom session line,
  the `/memory status` root and the persisted session row.

Harness shape (real binary, real ConPTY via pywinpty, no real model calls):
  1. Isolated HOME + isolated workdir; a mock provider config so the binary
     reaches the interactive prompt without the provider picker. Only slash
     commands are driven — no prompt is ever sent to the model.
  2. A user-layer profile fixture is planted at
     `<home>/.aicli/profiles/<PROFILE_NAME>` (root OUTSIDE the workdir, so a
     workspace drift is unambiguous).
  3. Drive: `/memory status` -> `/profile use <name>` -> `/memory status`.
  4. Three independent evidence channels, all feeding `resolveLocalWorkspacePath`
     / `resolveChatStatusCurrentDirectory` (the fixed code path):
       a) `/memory status` prints `项目记忆 root=<workspace>/.aicli/memory`;
       b) the bottom session line (full cwd) repainted right after the switch;
       c) the persisted `workspace_path` in
          `~/.aicli/sessions/session_history.sqlite` (metadata_json.context).

Modes (red/green contrast proves the assertions can actually fail):
  green — binary built from a tree WITH the fix:
      all checks must pass; workspace stays <workdir> on all three channels.
  red   — binary built from the pre-fix baseline (cd044c9f):
      the three channels must show the drift to the profile dir.

Usage:
  # green (current tree): build first, then run from the repo root.
  cd backend
  go build -o dist/aicli-wsa-e2e.exe ./cmd/aicli
  cd ..
  python backend/scripts/profile-workspace-anchor-conpty-test.py \
      --exe backend/dist/aicli-wsa-e2e.exe --expect green

  # red harness self-check (pre-fix baseline):
  git worktree add tmp/wt-red cd044c9f
  cd tmp/wt-red/backend
  go build -o ../../../backend/dist/aicli-wsa-red.exe ./cmd/aicli
  cd ../../..   # back to repo root
  python backend/scripts/profile-workspace-anchor-conpty-test.py \
      --exe backend/dist/aicli-wsa-red.exe --expect red

  # or let the script build the current tree itself:
  python backend/scripts/profile-workspace-anchor-conpty-test.py --build --expect green

Requirements: Windows 10 1809+ (ConPTY), `pip install pywinpty`, Go toolchain
(only for --build). Exit code: 0 = all checks pass, 1 = any check failed.

Notes for maintainers:
  - `/status` is deliberately NOT used: in the TUI it opens a full-screen
    scroll overlay whose "q / Esc" close key cannot be delivered reliably on
    ConPTY in line-mode, and it swallows all later input.
  - Temp HOME/workdir are kept after the run (paths are printed and recorded
    in result.json) so the sqlite evidence stays inspectable.
"""
import argparse
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from winpty import PtyProcess

sys.stdout.reconfigure(encoding="utf-8", errors="replace", line_buffering=True)

SEND_ENTER = "\r"

PROFILE_NAME = "e2e-ws-anchor"

PROFILE_YAML = """profile:
  name: e2e-ws-anchor
  default_agent: coder
agents:
  coder:
    model: fixture
"""

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BACKEND_DIR = os.path.join(REPO_ROOT, "backend")
DEFAULT_EXE = os.path.join(BACKEND_DIR, "dist", "aicli-wsa-e2e.exe")
OUTPUT_ROOT = os.path.join(REPO_ROOT, "output", "conpty-ws-anchor")


class MockHandler(BaseHTTPRequestHandler):
    """Minimal OpenAI-compatible stub; never actually reached in this scenario."""

    protocol_version = "HTTP/1.0"

    def log_message(self, *args):
        pass

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def _reply(self, payload: dict):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        self._body()
        self._reply({
            "id": "mock",
            "object": "chat.completion",
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "ok"},
                "finish_reason": "stop",
            }],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        })

    def do_GET(self):
        self._reply({})


class PtyRecorder:
    """Accumulates ConPTY output in a daemon thread (read() blocks)."""

    def __init__(self, pty):
        self.pty = pty
        self.chunks = []
        self.lock = threading.Lock()
        self.thread = threading.Thread(target=self._loop, daemon=True)
        self.thread.start()

    def _loop(self):
        while True:
            try:
                chunk = self.pty.read(4096)
            except Exception:
                return
            if chunk:
                with self.lock:
                    self.chunks.append(chunk)
            elif not self.pty.isalive():
                return

    def text(self) -> str:
        with self.lock:
            return "".join(self.chunks)


def visible(text: str) -> str:
    """Cheap VT scrub for pattern matching: drop ESC sequences, keep text."""
    out = []
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch == "\x1b":
            j = i + 1
            if j < n and text[j] == "[":
                j += 1
                while j < n and not ("@" <= text[j] <= "~"):
                    j += 1
            elif j < n and text[j] == "]":
                j += 1
                while j < n and text[j] not in ("\x07", "\x1b"):
                    j += 1
                j += 1
            i = j + 1
            continue
        if ch >= " " or ch in "\r\n\t":
            out.append(ch)
        i += 1
    return "".join(out)


def wait_for(rec: PtyRecorder, *patterns, since=0, deadline=30.0, hard_deadline=None):
    """Wait until output after `since` contains any pattern; (ok, suffix)."""
    end = time.time() + deadline
    if hard_deadline:
        end = min(end, hard_deadline)
    while time.time() < end:
        text = visible(rec.text())
        suffix = text[since:]
        for pat in patterns:
            if pat in suffix:
                return True, suffix
        time.sleep(0.25)
    return False, visible(rec.text())[since:]


def norm_path(p: str) -> str:
    """Normalize a Windows path for comparisons (case-insensitive)."""
    return os.path.normcase(os.path.normpath(p.strip().strip('"')))


def compact(text: str) -> str:
    """Drop whitespace/box-drawing chars so wrap breaks don't break substrings."""
    drop = set(" \t\r\n│┃╭╮╰╯─━═")
    return "".join(ch for ch in text if ch not in drop)


def compact_low(text: str) -> str:
    return compact(text).lower()


def extract_last_root(text: str):
    """Extract the last `root=<path>` from a /memory status screen window.

    The TUI repaints the whole dialog on redraw (repeated text) so take the
    last occurrence; the next line (`notes=...`) has no newline separator in
    the raw TTY stream, hence the explicit cut.
    """
    marker = "root="
    idx = text.rfind(marker)
    if idx < 0:
        return None
    rest = text[idx + len(marker):]
    notes = rest.find("notes=")
    if notes >= 0:
        rest = rest[:notes]
    out = []
    for ch in rest:
        if ch in ("\r", "\n", "│", "┃", " "):
            break
        out.append(ch)
    value = "".join(out).strip()
    return value or None


def read_persisted_workspace(home: str):
    """Read workspace_path from the session store's metadata_json.context."""
    db = os.path.join(home, ".aicli", "sessions", "session_history.sqlite")
    if not os.path.isfile(db):
        return None
    persisted = None
    con = sqlite3.connect(db)
    try:
        cur = con.cursor()
        for (meta,) in cur.execute("SELECT metadata_json FROM sessions").fetchall():
            if isinstance(meta, bytes):
                meta = meta.decode("utf-8", "replace")
            try:
                obj = json.loads(meta or "{}")
            except Exception:  # noqa: BLE001
                obj = {}
            ws = (obj.get("context") or {}).get("workspace_path")
            if ws:
                persisted = ws
    finally:
        con.close()
    return persisted


def main() -> int:
    ap = argparse.ArgumentParser(description="ConPTY E2E: /profile use must not move the session workspace.")
    ap.add_argument(
        "--exe",
        default=os.environ.get("AICLI_WSA_E2E_EXE", DEFAULT_EXE),
        help=f"aicli binary under test (default: {DEFAULT_EXE} / env AICLI_WSA_E2E_EXE)",
    )
    ap.add_argument("--build", action="store_true",
                    help="go build the current tree into backend/dist before running")
    ap.add_argument("--expect", choices=["green", "red"], required=True)
    ap.add_argument("--total-deadline", type=float, default=240.0)
    ap.add_argument("--out-dir", default="",
                    help="evidence dir (default: output/conpty-ws-anchor/<ts>-<expect>)")
    args = ap.parse_args()

    if args.build:
        os.makedirs(os.path.dirname(DEFAULT_EXE), exist_ok=True)
        print(f"[build] go build -o {DEFAULT_EXE} ./cmd/aicli (cwd={BACKEND_DIR})")
        rc = subprocess.call(["go", "build", "-o", DEFAULT_EXE, "./cmd/aicli"], cwd=BACKEND_DIR)
        if rc != 0:
            print("[build] FAILED rc=%d" % rc)
            return 2
        args.exe = DEFAULT_EXE

    if not os.path.isfile(args.exe):
        print(f"[setup] exe not found: {args.exe}")
        print("        build it first (see --build / usage in the script header).")
        return 2

    hard_deadline = time.time() + args.total_deadline

    home = tempfile.mkdtemp(prefix="aicli-wsa-home-")
    workdir = tempfile.mkdtemp(prefix="aicli-wsa-cwd-")
    os.makedirs(os.path.join(home, ".aicli"), exist_ok=True)

    out_dir = args.out_dir or os.path.join(
        OUTPUT_ROOT, time.strftime("%Y%m%d-%H%M%S") + "-" + args.expect
    )
    os.makedirs(out_dir, exist_ok=True)

    # Mock provider: avoids the startup provider picker; never actually called
    # (only slash commands are driven).
    srv = ThreadingHTTPServer(("127.0.0.1", 0), MockHandler)
    port = srv.server_address[1]
    threading.Thread(target=srv.serve_forever, daemon=True).start()

    config = f"""aicli:
  chat:
    default_provider: mock
    default_model: m1
    stream: false
providers:
  default_provider: mock
  items:
    mock:
      enabled: true
      protocol: openai
      base_url: http://127.0.0.1:{port}
      api_path: /v1/chat/completions
      api_key: mock-key
      default_model: m1
"""
    with open(os.path.join(home, ".aicli", "config.yaml"), "w", encoding="utf-8") as fh:
        fh.write(config)

    # User-layer profile fixture (LayerRoot("user") = <home>/.aicli/profiles).
    # Its root is OUTSIDE the workdir: any workspace drift is unambiguous.
    profile_root = os.path.join(home, ".aicli", "profiles", PROFILE_NAME)
    os.makedirs(os.path.join(profile_root, "agents", "coder", "prompts"), exist_ok=True)
    os.makedirs(os.path.join(profile_root, "agents", "coder", "sessions"), exist_ok=True)
    with open(os.path.join(profile_root, "profile.yaml"), "w", encoding="utf-8") as fh:
        fh.write(PROFILE_YAML)
    with open(os.path.join(profile_root, "runtime.yaml"), "w", encoding="utf-8") as fh:
        fh.write("agent:\n  defaultModel: fixture\n")
    with open(os.path.join(profile_root, "mcp.yaml"), "w", encoding="utf-8") as fh:
        fh.write("mcp_servers: {}\n")
    with open(
        os.path.join(profile_root, "agents", "coder", "prompts", "system.md"),
        "w", encoding="utf-8",
    ) as fh:
        fh.write("E2E workspace anchor profile prompt.\n")

    expect_memory_root = os.path.join(workdir, ".aicli", "memory")
    profile_memory_root = os.path.join(profile_root, ".aicli", "memory")

    # AICLI_CHAT_INPUT_LINE_MODE=1 is the documented ConPTY E2E seam: under a
    # pseudo console the per-key editor cannot be driven by raw byte injection,
    # so input falls back to the deterministic line queue.
    env = {
        **os.environ,
        "USERPROFILE": home,
        "HOME": home,
        "AICLI_FOLDER_TRUST": "1",
        "AICLI_CHAT_INPUT_LINE_MODE": "1",
    }

    checks = []

    def check(name, ok, detail=""):
        checks.append({"name": name, "ok": bool(ok)})
        mark = "PASS" if ok else "FAIL"
        print(f"[{mark}] {name}")
        if not ok and detail:
            print("  detail: " + detail[-2000:].replace("\n", " | "))

    print(f"[setup] exe={args.exe}")
    print(f"[setup] home={home}")
    print(f"[setup] workdir={workdir}")
    print(f"[setup] profile_root={profile_root}")

    pty = PtyProcess.spawn(
        [args.exe, "chat", "--trust"],
        cwd=workdir,
        dimensions=(50, 160),
        env=env,
    )
    rec = PtyRecorder(pty)

    def send(line: str):
        pty.write(line + SEND_ENTER)

    try:
        # ---- 0. Ready: interactive prompt reached ----
        ready, ready_text = wait_for(
            rec, "快捷操作", "输入 /help", "Plan OFF", "AI CLI",
            since=0, deadline=90.0, hard_deadline=hard_deadline,
        )
        check("startup: chat reached interactive prompt", ready, ready_text)

        # ---- 1. Before switch: /memory status root == <workdir>/.aicli/memory --
        mark = len(visible(rec.text()))
        send("/memory status")
        got, win1 = wait_for(rec, "项目记忆 root=", since=mark, deadline=45.0,
                             hard_deadline=hard_deadline)
        root1 = extract_last_root(win1) if got else None
        check("before switch: /memory status root captured", bool(root1), win1)
        check(
            "before switch: root == <workdir>/.aicli/memory",
            root1 is not None and norm_path(root1) == norm_path(expect_memory_root),
            f"root1={root1!r} want={expect_memory_root!r}",
        )

        # ---- 2. Switch: /profile use <name> -----------------------------------
        mark = len(visible(rec.text()))
        send(f"/profile use {PROFILE_NAME}")
        got, win2 = wait_for(rec, "已切换 profile", since=mark, deadline=60.0,
                             hard_deadline=hard_deadline)
        check("switch: /profile use report seen", got, win2)
        check("switch: target profile in report", PROFILE_NAME in win2, win2[-800:])
        # Channel (b): the bottom session line carries the full cwd and is
        # repainted immediately (phase 5 RefreshStatus) after the switch.
        if args.expect == "green":
            check(
                "switch: bottom session line stays <workdir>",
                compact_low(norm_path(workdir)) in compact_low(win2),
                win2[-1200:],
            )
        else:
            check(
                "red: bottom session line drifted to profile dir",
                compact_low(norm_path(profile_root)) in compact_low(win2),
                win2[-1200:],
            )

        # ---- 3. After switch: /memory status ----------------------------------
        mark = len(visible(rec.text()))
        send("/memory status")
        got, win3 = wait_for(rec, "项目记忆 root=", since=mark, deadline=45.0,
                             hard_deadline=hard_deadline)
        root2 = extract_last_root(win3) if got else None
        check("after switch: /memory status root captured", bool(root2), win3)

        # ---- 4. Channel (a) verdict ------------------------------------------
        if args.expect == "green":
            check(
                "after switch: /memory status root unchanged (<workdir>)",
                root2 is not None and norm_path(root2) == norm_path(expect_memory_root),
                f"root2={root2!r} want={expect_memory_root!r}",
            )
            check(
                "after switch: root NOT dragged to profile dir",
                root2 is not None and norm_path(profile_root) not in norm_path(root2),
                f"root2={root2!r} profile_root={profile_root!r}",
            )
        else:
            check(
                "red: /memory status root drifted to profile dir (bug reproduced)",
                root2 is not None and norm_path(root2) == norm_path(profile_memory_root),
                f"root2={root2!r} want={profile_memory_root!r}",
            )

    finally:
        try:
            send("/exit")
            for _ in range(40):
                if not pty.isalive():
                    break
                time.sleep(0.25)
        except Exception:
            pass
        if pty.isalive():
            try:
                pty.terminate(force=True)
            except Exception:
                pass
        srv.shutdown()

    # ---- 5. Channel (c): persisted workspace_path in the session store --------
    # The switch writes sessionmeta during its phase 4 (independent of exit),
    # so the durable row is stable by the time the process is gone.
    persisted = None
    try:
        persisted = read_persisted_workspace(home)
    except Exception as exc:  # noqa: BLE001
        check("persisted: session store readable", False, str(exc))

    check("persisted: session workspace_path captured", bool(persisted), repr(persisted))
    if args.expect == "green":
        check(
            "persisted: workspace_path == <workdir>",
            persisted is not None and norm_path(persisted) == norm_path(workdir),
            f"persisted={persisted!r} want={workdir!r}",
        )
    else:
        check(
            "red: persisted workspace_path drifted to profile dir",
            persisted is not None and norm_path(persisted) == norm_path(profile_root),
            f"persisted={persisted!r} want={profile_root!r}",
        )

    transcript = visible(rec.text())
    with open(os.path.join(out_dir, "transcript.txt"), "w", encoding="utf-8") as fh:
        fh.write(transcript)

    ok_all = all(c["ok"] for c in checks)
    summary = {
        "exe": args.exe,
        "expect": args.expect,
        "home": home,
        "workdir": workdir,
        "profile_root": profile_root,
        "checks": checks,
        "all_ok": ok_all,
    }
    with open(os.path.join(out_dir, "result.json"), "w", encoding="utf-8") as fh:
        json.dump(summary, fh, ensure_ascii=False, indent=2)

    print(f"[evidence] output_dir={out_dir}")
    print(f"[result] expect={args.expect} all_ok={ok_all}")
    return 0 if ok_all else 1


if __name__ == "__main__":
    sys.exit(main())