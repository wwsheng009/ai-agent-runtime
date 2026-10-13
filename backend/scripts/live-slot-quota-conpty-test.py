"""ConPTY E2E for the spawn_subagents live-slot quota gate.

Scenario (deterministic, no real provider):
  1. aicli chat (real binary, real ConPTY via pywinpty) gets one prompt.
  2. The mock provider makes the parent call spawn_subagents with N tasks while
     agents.maxThreads is set to M.
  3. The gate must decide BEFORE any durable row is written:
       - N > M  -> the tool result carries the actionable limit diagnostic
                   ("thread limit reached", max_threads=M, next_action=...).
       - N <= M -> the tool result carries the batch handle ("batch_id").
  4. The parent ends the turn with a phase marker we can read off the screen.

Verdict is taken from the mock request log (authoritative, immune to terminal
repaint) plus the phase marker on the ConPTY screen.

Usage:
  python backend/scripts/live-slot-quota-conpty-test.py --expect reject
  python backend/scripts/live-slot-quota-conpty-test.py --expect admit --max-threads 6 --agents 2
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
CHILD_GOAL_MARKER = "MOCK-CHILD-GOAL"
LIMIT_MARKER = "thread limit reached"
INITIAL_PROMPT = "运行 quota 场景：请调用 spawn_subagents 派发后台子代理任务，然后结束回合。"

MOCK_LOG = []
MOCK_LOCK = threading.Lock()


def record(**fields):
    with MOCK_LOCK:
        row = {"t": time.strftime("%H:%M:%S")}
        row.update(fields)
        MOCK_LOG.append(row)


def spawn_args(count):
    agents = []
    for i in range(count):
        agents.append(
            {
                "id": "mock-child-%d" % (i + 1),
                "goal": CHILD_GOAL_MARKER + "-%d：只回复 CHILD-DONE，不要调用任何工具。" % (i + 1),
                "difficulty": "easy",
                "task_type": "generate",
                "read_only": True,
            }
        )
    return {"agents": agents}


class MockHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"
    task_count = 2
    child_delay = 0.0
    scenario = "single"

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
        last_role = msgs[-1].get("role") if msgs else ""
        last_content = str(msgs[-1].get("content")) if msgs else ""
        spawn_calls = sum(
            1
            for m in msgs
            if m.get("role") == "assistant"
            and any(
                (t.get("function") or {}).get("name") == "spawn_subagents"
                for t in (m.get("tool_calls") or [])
            )
        )
        child_marker = any(
            m.get("role") == "user" and CHILD_GOAL_MARKER in str(m.get("content")) for m in msgs
        )
        limit_seen = any(
            m.get("role") == "tool" and LIMIT_MARKER in str(m.get("content")) for m in msgs
        )
        batch_seen = any(
            m.get("role") == "tool" and "batch_id" in str(m.get("content")) for m in msgs
        )
        limit_detail = ""
        batch_detail = ""
        for m in msgs:
            if m.get("role") != "tool":
                continue
            text = str(m.get("content"))
            if not limit_detail and LIMIT_MARKER in text:
                limit_detail = text[:800]
            if not batch_detail and "batch_id" in text:
                batch_detail = text[:400]

        content, tool_call, finish = "MOCK-FALLBACK", None, "stop"
        kind = "fallback"
        if self.scenario == "cumulative":
            # Turn 1 spawns batch #1; the still-live children make batch #2
            # exceed the ceiling, so the gate must reject the second dispatch.
            if child_marker:
                kind = "child"
                if self.child_delay:
                    time.sleep(self.child_delay)
                content = "CHILD-DONE 子任务已完成。"
            elif last_role == "tool" and limit_seen:
                kind = "rejected2"
                content = "QUOTA-CUMULATIVE-REJECTED-OK 第二个批次被存活槽位上限拒绝。"
            elif last_role == "tool" and batch_seen and spawn_calls < 2:
                kind = "spawn2"
                content = "SPAWNING-2 派发第二个后台批次。"
                tool_call = ("call_quota_2", spawn_args(self.task_count))
                finish = "tool_calls"
            elif spawn_calls == 0:
                kind = "spawn1"
                content = "SPAWNING-1 派发第一个后台批次。"
                tool_call = ("call_quota_1", spawn_args(self.task_count))
                finish = "tool_calls"
            else:
                kind = "wrap"
        elif child_marker:
            kind = "child"
            if self.child_delay:
                time.sleep(self.child_delay)
            content = "CHILD-DONE 子任务已完成。"
        elif last_role == "tool":
            if limit_seen:
                kind = "rejected"
                content = "QUOTA-REJECTED-OK 批次被存活槽位上限拒绝。"
            elif batch_seen:
                kind = "admitted"
                content = "QUOTA-ADMITTED-OK 批次已派发。"
            else:
                kind = "tool-other"
                content = "TOOL-RESULT-OTHER 未识别的工具回执。"
        elif spawn_calls == 0:
            kind = "spawn"
            content = "SPAWNING 正在派发 %d 个子代理任务。" % self.task_count
            tool_call = ("call_quota", spawn_args(self.task_count))
            finish = "tool_calls"
        else:
            kind = "wrap"

        record(
            kind=kind,
            stream=stream,
            spawn_calls=spawn_calls,
            child_marker=child_marker,
            limit_seen=limit_seen,
            batch_seen=batch_seen,
            limit_detail=limit_detail,
            batch_detail=batch_detail,
            last_role=last_role,
            last_content=last_content[-400:],
        )

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


def wait_signal(rec, pattern, deadline, hard_deadline):
    end = min(time.time() + deadline, hard_deadline)
    while time.time() < end:
        if pattern in rec.text():
            return True
        time.sleep(0.25)
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--exe",
        default=os.environ.get(
            "AICLI_QUOTA_E2E_EXE",
            r"E:\projects\ai\ai-agent-runtime\backend\aicli-quota.exe",
        ),
    )
    ap.add_argument("--max-threads", type=int, default=1)
    ap.add_argument("--agents", type=int, default=2)
    ap.add_argument("--expect", choices=["reject", "admit"], default="reject")
    ap.add_argument("--scenario", choices=["single", "cumulative"], default="single")
    ap.add_argument("--child-delay", type=float, default=0.0)
    ap.add_argument("--deadline", type=float, default=120)
    ap.add_argument("--total-deadline", type=float, default=240)
    args = ap.parse_args()

    hard_deadline = time.time() + args.total_deadline
    MockHandler.task_count = args.agents
    MockHandler.child_delay = args.child_delay
    MockHandler.scenario = args.scenario

    out_dir = os.path.join(
        r"E:\projects\ai\ai-agent-runtime\output\conpty-live-slot-quota",
        time.strftime("%Y%m%d-%H%M%S"),
    )
    os.makedirs(out_dir, exist_ok=True)

    home = tempfile.mkdtemp(prefix="aicli-quota-e2e-home-")
    workdir = tempfile.mkdtemp(prefix="aicli-quota-e2e-cwd-")
    os.makedirs(os.path.join(home, ".aicli"), exist_ok=True)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), MockHandler)
    port = srv.server_address[1]
    threading.Thread(target=srv.serve_forever, daemon=True).start()

    config = f"""aicli:
  chat:
    default_provider: mock
    default_model: m1
    stream: false
agents:
  maxThreads: {args.max_threads}
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

    # agents.* belongs to the runtime.yaml layer ($HOME/.aicli/runtime.yaml), NOT
    # config.yaml: the global config loader warns and ignores runtime-only keys.
    runtime_config = f"""agents:
  maxThreads: {args.max_threads}
"""
    with open(os.path.join(home, ".aicli", "runtime.yaml"), "w", encoding="utf-8") as fh:
        fh.write(runtime_config)

    env = {**os.environ, "USERPROFILE": home, "HOME": home, "AICLI_FOLDER_TRUST": "1"}
    steps = []

    def check(name, ok, detail=""):
        steps.append({"name": name, "ok": bool(ok)})
        print(("[PASS] " if ok else "[FAIL] ") + name)
        if not ok:
            print("  detail: " + (detail or "")[-2000:].replace("\n", " | "))

    pty = PtyProcess.spawn(
        [args.exe, "chat", "--yolo", "--trust", "--prompt", INITIAL_PROMPT],
        cwd=workdir,
        dimensions=(50, 150),
        env=env,
    )
    rec = PtyRecorder(pty)

    if args.scenario == "cumulative":
        marker = "QUOTA-CUMULATIVE-REJECTED-OK"
        wait_signal(rec, marker, args.deadline, hard_deadline)

        text = rec.text()
        kinds = [r["kind"] for r in MOCK_LOG]
        first_tool = next((r.get("batch_detail", "") for r in MOCK_LOG if r.get("kind") == "spawn2"), "")
        second_tool = next((r.get("limit_detail", "") for r in MOCK_LOG if r.get("kind") == "rejected2"), "")

        check("mock: parent dispatched first batch", "spawn1" in kinds, "kinds=%s" % kinds)
        check("mock: first batch was admitted (still live)", "batch_id" in first_tool, first_tool)
        check("mock: second batch rejected by the cumulative live ceiling", "rejected2" in kinds,
              "kinds=%s" % kinds)
        check("mock: rejection names max_threads=%d and active_children=%d"
              % (args.max_threads, args.agents),
              ("max_threads=%d" % args.max_threads) in second_tool
              and ("active_children=%d" % args.agents) in second_tool, second_tool)
        check("mock: rejection carried the limit diagnostic", LIMIT_MARKER in second_tool, second_tool)
        check("mock: rejection carried next_action", "next_action=" in second_tool, second_tool)
        check("screen: cumulative marker rendered (%s)" % marker, marker in text)
    else:
        marker = "QUOTA-REJECTED-OK" if args.expect == "reject" else "QUOTA-ADMITTED-OK"
        wait_signal(rec, marker, args.deadline, hard_deadline)

        text = rec.text()
        kinds = [r["kind"] for r in MOCK_LOG]
        if args.expect == "reject":
            last_tool = next((r.get("limit_detail", "") for r in MOCK_LOG if r.get("kind") == "rejected"), "")
        else:
            last_tool = next((r.get("batch_detail", "") for r in MOCK_LOG if r.get("kind") == "admitted"), "")

        check("mock: parent called spawn_subagents", "spawn" in kinds)
        if args.expect == "reject":
            check("mock: batch was rejected by the live-slot gate", "rejected" in kinds,
                  "kinds=%s" % kinds)
            check("mock: rejection carried the limit diagnostic", LIMIT_MARKER in last_tool, last_tool)
            check("mock: rejection named max_threads=%d" % args.max_threads,
                  ("max_threads=%d" % args.max_threads) in last_tool, last_tool)
            check("mock: rejection carried next_action", "next_action=" in last_tool, last_tool)
        else:
            check("mock: batch was admitted", "admitted" in kinds, "kinds=%s" % kinds)
            check("mock: admission returned a batch handle", "batch_id" in last_tool, last_tool)
        check("screen: phase marker rendered (%s)" % marker, marker in text)

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
        fh.write(text)
    with open(os.path.join(out_dir, "mock-requests.jsonl"), "w", encoding="utf-8") as fh:
        for row in MOCK_LOG:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")

    failed = [s for s in steps if not s["ok"]]
    print("EXE:", args.exe)
    print("CONFIG: maxThreads=%d agents=%d expect=%s" % (args.max_threads, args.agents, args.expect))
    print("ARTIFACTS:", out_dir)
    print(("RESULT: PASS" if not failed else "RESULT: FAIL") + f" (failed_steps={[s['name'] for s in failed]})")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
