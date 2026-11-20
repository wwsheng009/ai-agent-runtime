"""ConPTY E2E for Agent Memory v2.

Starts the real aicli binary in a Windows pseudo console, runs one real
provider turn in an isolated workspace, sends /memory status and /exit, then
starts a second session in the same workspace to verify cross-session recall.
Finally it reads the workspace memory SQLite database and provider request
artifacts.

This validates the host path that unit tests cannot prove:
  real terminal -> SessionActor -> Turn DigestSink -> memory store
  -> digest maintenance -> phase1 extraction -> phase2/records
  -> next session memory injection -> model recall

Usage:
  python backend/scripts/memory-conpty-e2e.py
  python backend/scripts/memory-conpty-e2e.py --provider magpie \
    --model auto/deepseek-v4.1-flash-1017 --reasoning max
"""
import argparse
import glob
import json
import os
import re
import sqlite3
import sys
import tempfile
import threading
import time

from winpty import PtyProcess

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

SEND_ENTER = "\r"
MARKER = "MEMORY-CONPTY-OK"
RECALL_MARKER = "MEMORY-RECALL-OK"
PROMPT = (
    "这是一个 Agent Memory ConPTY 验证。请记住以下项目约定：\n"
    "- 项目代号：MEMCONPTY-20261117\n"
    "- 发布分支：release/memconpty\n"
    "- 部署命令：make deploy-memconpty\n"
    "完成后只回复：" + MARKER
)
RECALL_PROMPT = (
    "请只根据跨会话项目记忆回答，不要使用任何工具。回忆并输出：\n"
    "CODE=<项目代号>\n"
    "BRANCH=<发布分支>\n"
    "DEPLOY=<部署命令>\n"
    "最后一行输出：" + RECALL_MARKER
)


def chat_command(args, session_dir, log_dir, prompt):
    return [
        args.exe,
        "chat",
        "--yolo",
        "--trust",
        "--headless",
        "--no-skills",
        "--disable-tools",
        "--mesh=false",
        "--session-dir",
        session_dir,
        "--log-dir",
        log_dir,
        "--provider",
        args.provider,
        "--model",
        args.model,
        "--reasoning-effort",
        args.reasoning,
        "--prompt",
        prompt,
    ]


def find_session_dir(log_dir, initial_message):
    for chat_path in glob.glob(os.path.join(log_dir, "**", "chat", "chat.json"), recursive=True):
        try:
            with open(chat_path, encoding="utf-8") as fh:
                data = json.load(fh)
        except Exception:
            continue
        if data.get("initial_message") == initial_message:
            return os.path.dirname(os.path.dirname(chat_path))
    return ""


def request_contains(session_dir, needles):
    for path in glob.glob(os.path.join(session_dir, "http", "*_request_provider_wrapper.json")):
        try:
            text = open(path, encoding="utf-8", errors="ignore").read()
        except Exception:
            continue
        if all(needle in text for needle in needles):
            return True
    return False


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


def wait_for(rec, needle, deadline):
    while time.time() < deadline:
        if needle in rec.text():
            return True
        time.sleep(0.25)
    return False


def wait_for_exit(pty, timeout):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not pty.isalive():
            return True
        time.sleep(0.25)
    return not pty.isalive()


def memory_snapshot(db_path):
    snapshot = {
        "db_path": db_path,
        "exists": os.path.exists(db_path),
        "schema_version": 0,
        "digests": {},
        "jobs": [],
        "entries": [],
        "summaries": [],
    }
    if not snapshot["exists"]:
        return snapshot
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        try:
            snapshot["schema_version"] = con.execute(
                "select coalesce(max(version),0) from schema_migrations"
            ).fetchone()[0]
        except Exception:
            pass
        for status, count in con.execute(
            "select status, count(*) from memory_turn_digests group by status"
        ):
            snapshot["digests"][status] = count
        snapshot["jobs"] = [
            dict(zip(("kind", "status", "attempt", "last_error"), row))
            for row in con.execute(
                "select kind, status, attempt, last_error from memory_jobs order by created_at"
            )
        ]
        snapshot["entries"] = [
            dict(zip(("id", "kind", "status", "text"), row))
            for row in con.execute(
                "select id, kind, status, text from memory_entries order by updated_at desc limit 20"
            )
        ]
        snapshot["summaries"] = [
            dict(zip(("id", "status", "freshness_status", "model"), row))
            for row in con.execute(
                "select id, status, freshness_status, model from memory_summaries "
                "order by revision desc limit 5"
            )
        ]
    finally:
        con.close()
    return snapshot


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--exe",
        default=os.environ.get(
            "AICLI_MEMORY_CONPTY_EXE",
            r"E:\projects\ai\ai-agent-runtime\backend\aicli.exe",
        ),
    )
    ap.add_argument("--provider", default="magpie")
    ap.add_argument("--model", default="auto/deepseek-v4.1-flash-1017")
    ap.add_argument("--reasoning", default="max")
    ap.add_argument("--timeout", type=float, default=180)
    ap.add_argument(
        "--skip-recall",
        action="store_true",
        help="Only run the first session (digest/extraction), skip cross-session recall.",
    )
    args = ap.parse_args()

    out_dir = os.path.join(
        r"E:\projects\ai\ai-agent-runtime\output\memory-conpty",
        time.strftime("%Y%m%d-%H%M%S"),
    )
    os.makedirs(out_dir, exist_ok=True)
    workspace = tempfile.mkdtemp(prefix="aicli-memory-conpty-ws-")
    session_dir = tempfile.mkdtemp(prefix="aicli-memory-conpty-sessions-")
    log_dir = tempfile.mkdtemp(prefix="aicli-memory-conpty-logs-")
    os.makedirs(os.path.join(workspace, ".aicli"), exist_ok=True)
    runtime_config = """memory:
  mode: on
  generation:
    enabled: true
    trigger: both
    minIdleHours: 0
    maxSessionsPerRun: 2
    turnDigest:
      enabled: true
      storeText: true
      recoveryOnStartup: true
      windowSummaryEnabled: false
      idleExtractMinutes: 1
  consolidation:
    enabled: true
  injection:
    enabled: true
    mode: hybrid
"""
    with open(os.path.join(workspace, ".aicli", "runtime.yaml"), "w", encoding="utf-8") as fh:
        fh.write(runtime_config)

    env = {**os.environ, "AICLI_FOLDER_TRUST": "1"}
    cmd = chat_command(args, session_dir, log_dir, PROMPT)
    print("EXE:", args.exe)
    print("MODEL:", args.provider, args.model, args.reasoning)
    print("WORKSPACE:", workspace)
    print("COMMAND:", " ".join(cmd))

    pty = PtyProcess.spawn(cmd, cwd=workspace, dimensions=(50, 160), env=env)
    rec = PtyRecorder(pty)
    deadline = time.time() + args.timeout
    turn_finished_seen = wait_for(rec, "agent.turn.finished", deadline)
    finalized_seen = wait_for(rec, "parent.turn.finalized", min(deadline, time.time() + 30))
    time.sleep(1.0)
    try:
        pty.write("/memory status" + SEND_ENTER)
    except Exception:
        pass
    status_seen = wait_for(rec, "digest_runtime", min(deadline, time.time() + 30))
    if not status_seen:
        status_seen = wait_for(rec, "runtime=ready", min(deadline, time.time() + 10))
    try:
        pty.write("/exit" + SEND_ENTER)
    except Exception:
        pass
    clean_exit = wait_for_exit(pty, 180)
    if pty.isalive():
        try:
            pty.terminate(force=True)
        except Exception:
            pass
        clean_exit = False

    screen = rec.text()
    marker_seen = MARKER in screen
    db_path = os.path.join(workspace, ".aicli", "memory", "memory.db")
    snapshot = memory_snapshot(db_path)
    with open(os.path.join(out_dir, "screen-visible.txt"), "w", encoding="utf-8") as fh:
        fh.write(screen)
    with open(os.path.join(out_dir, "memory-snapshot.json"), "w", encoding="utf-8") as fh:
        json.dump(snapshot, fh, ensure_ascii=False, indent=2)

    recall_marker_seen = True
    recall_values_seen = True
    injection_request_seen = True
    recall_session_dir = ""
    if not args.skip_recall:
        print("RECALL: starting second ConPTY session in the same workspace")
        recall_cmd = chat_command(args, session_dir, log_dir, RECALL_PROMPT)
        recall_pty = PtyProcess.spawn(
            recall_cmd, cwd=workspace, dimensions=(50, 160), env=env
        )
        recall_rec = PtyRecorder(recall_pty)
        recall_deadline = time.time() + args.timeout
        # The recall prompt itself does not contain the values. Waiting for one
        # actual value proves the model produced a recalled answer rather than
        # merely echoing the prompt.
        recall_turn_seen = wait_for(recall_rec, "MEMCONPTY-20261117", recall_deadline)
        recall_finalized_seen = wait_for(
            recall_rec,
            "parent.turn.finalized",
            min(recall_deadline, time.time() + 30),
        )
        time.sleep(1.0)
        try:
            recall_pty.write("/exit" + SEND_ENTER)
        except Exception:
            pass
        recall_clean_exit = wait_for_exit(recall_pty, 180)
        if recall_pty.isalive():
            try:
                recall_pty.terminate(force=True)
            except Exception:
                pass
            recall_clean_exit = False

        recall_screen = recall_rec.text()
        with open(
            os.path.join(out_dir, "screen-recall-visible.txt"),
            "w",
            encoding="utf-8",
        ) as fh:
            fh.write(recall_screen)

        recall_session_dir = find_session_dir(log_dir, RECALL_PROMPT)
        if not recall_session_dir:
            candidates = glob.glob(
                os.path.join(log_dir, "**", "chat", "chat.json"), recursive=True
            )
            if candidates:
                chat_path = max(candidates, key=os.path.getmtime)
                recall_session_dir = os.path.dirname(os.path.dirname(chat_path))

        injection_request_seen = bool(recall_session_dir) and request_contains(
            recall_session_dir,
            [
                "Project durable memory",
                "MEMCONPTY-20261117",
                "release/memconpty",
                "make deploy-memconpty",
            ],
        )
        recall_values_seen = bool(recall_turn_seen) and all(
            re.search(pattern, recall_screen)
            for pattern in (
                r"CODE\s*=\s*MEMCONPTY-20261117",
                r"BRANCH\s*=\s*release/memconpty",
                r"DEPLOY\s*=\s*make deploy-memconpty",
            )
        )
        recall_marker_seen = RECALL_MARKER in recall_screen
        recall_evidence = {
            "session_dir": recall_session_dir,
            "request_contains_injected_memory": injection_request_seen,
            "finalized": recall_finalized_seen,
            "clean_exit": recall_clean_exit,
            "marker_seen": recall_marker_seen,
            "final_values": {
                "code": bool(re.search(r"CODE\s*=\s*MEMCONPTY-20261117", recall_screen)),
                "branch": bool(
                    re.search(r"BRANCH\s*=\s*release/memconpty", recall_screen)
                ),
                "deploy": bool(
                    re.search(r"DEPLOY\s*=\s*make deploy-memconpty", recall_screen)
                ),
            },
        }
        with open(
            os.path.join(out_dir, "recall-evidence.json"),
            "w",
            encoding="utf-8",
        ) as fh:
            json.dump(recall_evidence, fh, ensure_ascii=False, indent=2)

        snapshot = memory_snapshot(db_path)
        with open(
            os.path.join(out_dir, "memory-snapshot.json"),
            "w",
            encoding="utf-8",
        ) as fh:
            json.dump(snapshot, fh, ensure_ascii=False, indent=2)

    digest_total = sum(snapshot["digests"].values())
    phase1_succeeded = any(
        job["kind"] == "phase1_extract" and job["status"] == "succeeded"
        for job in snapshot["jobs"]
    )
    entries = snapshot["entries"]
    checks = [
        ("real provider turn finished in ConPTY", turn_finished_seen),
        ("first session finalized before commands", finalized_seen),
        ("first session exited cleanly", clean_exit),
        ("real provider turn reached ConPTY marker", marker_seen),
        ("/memory status rendered runtime/digest diagnostics", status_seen),
        ("turn digest rows were persisted", digest_total > 0),
        ("phase1 extraction succeeded", phase1_succeeded or len(entries) > 0),
        ("memory record/candidate exists", len(entries) > 0),
    ]
    if not args.skip_recall:
        checks.extend(
            [
                ("second session recalled durable memory values", recall_values_seen),
                ("second session finalized before exit", recall_finalized_seen),
                ("second session exited cleanly", recall_clean_exit),
                ("second session reached recall marker", recall_marker_seen),
                (
                    "provider request contained injected durable memory",
                    injection_request_seen,
                ),
            ]
        )
    failed = [name for name, ok in checks if not ok]
    for name, ok in checks:
        print(("[PASS] " if ok else "[FAIL] ") + name)
    print("DIGESTS:", snapshot["digests"])
    print("JOBS:", json.dumps(snapshot["jobs"], ensure_ascii=False))
    print("ENTRIES:", json.dumps(entries, ensure_ascii=False))
    if not args.skip_recall:
        print("RECALL SESSION:", recall_session_dir)
    print("ARTIFACTS:", out_dir)
    print(("RESULT: PASS" if not failed else "RESULT: FAIL") + f" failed={failed}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
