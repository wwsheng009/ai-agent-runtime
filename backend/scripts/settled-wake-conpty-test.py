"""ConPTY E2E for the settled-turn wake fix (double-park regression, 2026-10-13).

Scenario (deterministic, no real provider):
  1. aicli chat (real binary, real ConPTY via pywinpty) gets one prompt.
  2. The mock provider makes the parent spawn a background subagent batch and end
     the turn -> the session parks ("Waiting for subagents").
  3. The child finishes (mock delays it so the park definitely happens first).
     The settle wake must auto-resume the parent (NO user input).
  4. The resumed turn spawns a SECOND batch and parks again.
  5. The second settle must wake the parent AGAIN. Before the fix both settles
     derived the same notify key and the second wake was suppressed, leaving the
     session parked forever -> this phase times out (red).

Usage:
  python backend/scripts/settled-wake-conpty-test.py [--exe PATH] [--child-delay 10]
      [--phase3-deadline 150] [--total-deadline 420] [--expect red|green]
"""
import argparse
import json
import os
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from winpty import PtyProcess

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

SEND_ENTER = "\r"
INITIAL_PROMPT = (
    "运行 wake-fix 场景：请调用 spawn_subagents 派发一个后台子代理任务，然后结束回合。"
)

MOCK_LOG = []
MOCK_LOCK = threading.Lock()


def record(kind, stream, spawn_calls, preflight, child_marker, spawn_results, last_role):
    with MOCK_LOCK:
        MOCK_LOG.append(
            {
                "t": time.strftime("%H:%M:%S"),
                "kind": kind,
                "stream": stream,
                "spawn_calls": spawn_calls,
                "preflight": preflight,
                "child_marker": child_marker,
                "spawn_tool_results": spawn_results,
                "last_role": last_role,
            }
        )


def spawn_args(child_id, goal_marker):
    return {
        "agents": [
            {
                "id": child_id,
                "goal": goal_marker + "：只回复 CHILD-DONE，不要调用任何工具。",
                "difficulty": "easy",
                "task_type": "generate",
                "read_only": True,
            }
        ]
    }


class MockHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"
    child_delay = 10.0

    def log_message(self, *args):  # silence
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(n)
        try:
            req = json.loads(raw)
        except Exception:
            req = {}
        stream = bool(req.get("stream"))
        msgs = req.get("messages") or []
        blob = json.dumps(msgs, ensure_ascii=False)
        spawn_calls = sum(
            1
            for m in msgs
            if m.get("role") == "assistant"
            and any(
                (t.get("function") or {}).get("name") == "spawn_subagents"
                for t in (m.get("tool_calls") or [])
            )
        )
        spawn_results = sum(
            1
            for m in msgs
            if m.get("role") == "tool"
            and ("batch_id" in str(m.get("content")) or "execution_mode" in str(m.get("content")))
        )
        preflight = "[Child lifecycle preflight]" in blob
        child_marker = any(
            m.get("role") == "user" and "MOCK-CHILD-GOAL" in str(m.get("content")) for m in msgs
        )
        last_role = msgs[-1].get("role") if msgs else ""

        content, tool_call, finish = "MOCK-FALLBACK", None, "stop"
        if child_marker:
            kind = "child"
            time.sleep(self.child_delay)
            content = "CHILD-DONE 子任务已完成。"
            record(kind, stream, spawn_calls, preflight, child_marker, spawn_results, last_role)
            record("child_done", stream, spawn_calls, preflight, child_marker, spawn_results, last_role)
        elif preflight and last_role != "tool" and spawn_calls >= 2:
            kind = "wake2"
            content = "PHASE3-RESUMED-OK 第二次挂起后已被自动唤醒，全部完成。"
        elif preflight and last_role != "tool":
            kind = "wake1"
            content = "PHASE2-SPAWN-2 收到唤醒，继续派发第二个后台批次。"
            tool_call = ("call_phase2", spawn_args("mock-child-2", "MOCK-CHILD-GOAL-2"))
            finish = "tool_calls"
        elif spawn_calls >= 1:
            kind = "wrap%d" % spawn_calls
            content = "PHASE%d-WAIT-DONE 批次已派发，等待后台完成。" % spawn_calls
        else:
            kind = "spawn1"
            content = "PHASE1-SPAWN-1 已派发第一个后台子代理批次。"
            tool_call = ("call_phase1", spawn_args("mock-child-1", "MOCK-CHILD-GOAL-1"))
            finish = "tool_calls"
        if kind not in ("child", "child_done"):
            record(kind, stream, spawn_calls, preflight, child_marker, spawn_results, last_role)

        cid = "chatcmpl-mock"
        msg = {"role": "assistant", "content": content}
        if tool_call:
            tid, args = tool_call
            msg["tool_calls"] = [
                {
                    "id": tid,
                    "type": "function",
                    "function": {
                        "name": "spawn_subagents",
                        "arguments": json.dumps(args, ensure_ascii=False),
                    },
                }
            ]
        if stream:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def chunk(delta, fr):
                return {
                    "id": cid,
                    "object": "chat.completion.chunk",
                    "created": 1,
                    "model": "m1",
                    "choices": [{"index": 0, "delta": delta, "finish_reason": fr}],
                }

            self.wfile.write(("data: " + json.dumps(chunk(msg, None), ensure_ascii=False) + "\n\n").encode())
            self.wfile.write(("data: " + json.dumps(chunk({}, finish), ensure_ascii=False) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n")
        else:
            resp = {
                "id": cid,
                "object": "chat.completion",
                "created": 1,
                "model": "m1",
                "choices": [{"index": 0, "message": msg, "finish_reason": finish}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
            }
            data = json.dumps(resp, ensure_ascii=False).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        try:
            self.wfile.flush()
        except Exception:
            pass


def visible(text):
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
            i = j + 1
            continue
        if ch >= " " or ch in "\r\n\t":
            out.append(ch)
        i += 1
    return "".join(out)


class PtyRecorder:
    def __init__(self, pty):
        self.pty = pty
        self.chunks = []
        self.lock = threading.Lock()
        threading.Thread(target=self._loop, daemon=True).start()

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

    def text(self):
        with self.lock:
            return visible("".join(self.chunks))

    def since(self, mark):
        return self.text()[mark:]


def wait_signal(rec, pattern, mark, deadline, hard_deadline):
    end = min(time.time() + deadline, hard_deadline)
    while time.time() < end:
        if pattern in rec.since(mark):
            return True, len(rec.text())
        time.sleep(0.25)
    return False, len(rec.text())


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--exe", default=os.environ.get("AICLI_WAKE_E2E_EXE", r"E:\projects\ai\ai-agent-runtime\backend\aicli-4x.exe"))
    ap.add_argument("--child-delay", type=float, default=float(os.environ.get("AICLI_WAKE_E2E_CHILD_DELAY", "10")))
    ap.add_argument("--phase3-deadline", type=float, default=float(os.environ.get("AICLI_WAKE_E2E_PHASE3_DEADLINE", "150")))
    ap.add_argument("--total-deadline", type=float, default=float(os.environ.get("AICLI_WAKE_E2E_TOTAL", "420")))
    ap.add_argument("--expect", choices=["green", "red"], default="green")
    args = ap.parse_args()

    hard_deadline = time.time() + args.total_deadline
    MockHandler.child_delay = args.child_delay

    out_dir = os.path.join(
        r"E:\projects\ai\ai-agent-runtime\output\conpty-settled-wake",
        time.strftime("%Y%m%d-%H%M%S"),
    )
    os.makedirs(out_dir, exist_ok=True)

    home = tempfile.mkdtemp(prefix="aicli-wake-e2e-home-")
    workdir = tempfile.mkdtemp(prefix="aicli-wake-e2e-cwd-")
    os.makedirs(os.path.join(home, ".aicli"), exist_ok=True)

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
    config_path = os.path.join(home, ".aicli", "config.yaml")
    with open(config_path, "w", encoding="utf-8") as fh:
        fh.write(config)

    env = {**os.environ, "USERPROFILE": home, "HOME": home, "AICLI_FOLDER_TRUST": "1"}
    steps = []

    def check(name, ok, detail=""):
        steps.append({"name": name, "ok": bool(ok)})
        print(("[PASS] " if ok else "[FAIL] ") + name)
        if not ok:
            print("  detail: " + (detail or "")[-2500:].replace("\n", " | "))

    pty = PtyProcess.spawn(
        [args.exe, "chat", "--yolo", "--trust", "--prompt", INITIAL_PROMPT],
        cwd=workdir,
        dimensions=(50, 150),
        env=env,
    )
    rec = PtyRecorder(pty)

    wait_signal(rec, "PHASE1-SPAWN-1", 0, 120, hard_deadline)
    wait_signal(rec, "Waiting for subagents", 0, 90, hard_deadline)
    wait_signal(rec, "PHASE2-SPAWN-2", 0, args.phase3_deadline, hard_deadline)
    wait_signal(rec, "PHASE3-RESUMED-OK", 0, args.phase3_deadline, hard_deadline)

    # Verdict: mock request sequence is authoritative (immune to terminal
    # repaint artifacts); the screen check pins first-occurrence ordering.
    text = rec.text()
    kinds = [r["kind"] for r in MOCK_LOG]
    pos1 = text.find("PHASE1-SPAWN-1")
    pos_banner = text.find("Waiting for subagents")
    pos2 = text.find("PHASE2-SPAWN-2")
    pos3 = text.find("PHASE3-RESUMED-OK")
    order_ok = -1 < pos1 < pos_banner < pos2 < pos3
    banner_count = text.count("Waiting for subagents")
    check("mock: parent dispatched first batch (spawn1)", "spawn1" in kinds)
    check("mock: first settle produced a wake resume (wake1)", "wake1" in kinds)
    check("mock: second settle produced a wake resume (wake2, fix)", "wake2" in kinds)
    check(
        "screen: order PHASE1 < parked banner < PHASE2 < PHASE3",
        order_ok,
        "pos1=%d banner=%d pos2=%d pos3=%d" % (pos1, pos_banner, pos2, pos3),
    )
    check("screen: parked banner observed (>=2 renders)", banner_count >= 2, "count=%d" % banner_count)

    try:
        pty.write("/exit" + SEND_ENTER)
    except Exception:
        pass
    for _ in range(40):
        if not pty.isalive():
            break
        time.sleep(0.25)
    if pty.isalive():
        try:
            pty.terminate(force=True)
        except Exception:
            pass
    srv.shutdown()

    with open(os.path.join(out_dir, "screen-visible.txt"), "w", encoding="utf-8") as fh:
        fh.write(rec.text())
    with open(os.path.join(out_dir, "mock-requests.jsonl"), "w", encoding="utf-8") as fh:
        for row in MOCK_LOG:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")

    expected_fail = args.expect == "red"
    failed = [s for s in steps if not s["ok"]]
    verdict = (len(failed) == 0) if not expected_fail else (len(failed) > 0)
    print("EXE:", args.exe)
    print("MOCK requests:", json.dumps(MOCK_LOG, ensure_ascii=False))
    print("ARTIFACTS:", out_dir)
    print(("RESULT: PASS" if verdict else "RESULT: FAIL") + f" (expect={args.expect}, failed_steps={[s['name'] for s in failed]})")
    return 0 if verdict else 1


if __name__ == "__main__":
    sys.exit(main())
