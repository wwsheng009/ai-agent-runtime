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
  python backend/scripts/memory-conpty-e2e.py --exe <freshly-built-aicli.exe>
  python backend/scripts/memory-conpty-e2e.py --exe <freshly-built-aicli.exe> \
    --provider magpie \
    --model auto/deepseek-v4.1-flash-1017 --reasoning max
"""
import argparse
import atexit
import glob
import json
import os
import re
import shutil
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


def _empty_counts(*keys):
    return {
        "available": False,
        "total": 0,
        "by_" + "_".join(keys): {},
        "error_present": False,
    }


_SAFE_LABELS = {
    "state": {
        "captured",
        "materializing",
        "materialized",
        "deferred",
        "failed",
    },
    "status": {
        "pending",
        "processing",
        "done",
        "failed",
        "skipped",
        "invalidated",
        "candidate",
        "active",
        "superseded",
        "expired",
        "deleted",
        "running",
        "succeeded",
    },
    "rollup_status": {"pending", "done", "skipped", "failed"},
    "kind": {
        "fact",
        "decision",
        "preference",
        "procedure",
        "reference",
        "correction",
        "phase1_extract",
        "phase1_extract/v2",
        "phase1_rollup",
        "phase2_consolidate",
    },
    "source_kind": {
        "session_extract",
        "turn_window",
        "manual",
        "import",
        "correction",
        "consolidation",
        "agent",
    },
    "freshness_status": {"fresh", "stale", "unverified"},
}


def _safe_label(column, value):
    label = "" if value is None else str(value).strip().lower()
    return label if label in _SAFE_LABELS.get(column, set()) else "other"


def _group_counts(con, table, *columns):
    """Return only bounded counts; never return database errors or row data."""
    result = _empty_counts(*columns)
    try:
        quoted = ", ".join(columns)
        rows = con.execute(
            f"select {quoted}, count(*) from {table} group by {quoted}"
        )
        groups = {}
        for row in rows:
            group = tuple(
                _safe_label(column, value)
                for column, value in zip(columns, row[:-1])
            )
            key = group[0] if len(group) == 1 else "|".join(group)
            groups[key] = groups.get(key, 0) + int(row[-1])
        result["available"] = True
        result["total"] = sum(groups.values())
        result["by_" + "_".join(columns)] = groups
    except Exception:
        result["error_present"] = True
    return result


def _count_query(con, query):
    try:
        return int(con.execute(query).fetchone()[0]), False
    except Exception:
        return 0, True


def _find_session_database(session_dir):
    """Find the effective session SQLite file without exposing its path."""
    candidates = []
    for pattern in ("*.sqlite", "*.sqlite3", "*.db"):
        candidates.extend(
            glob.glob(os.path.join(session_dir, "**", pattern), recursive=True)
        )
    candidates.sort(
        key=lambda path: (
            os.path.basename(path) not in ("session_history.sqlite", "sessions.sqlite"),
            -os.path.getmtime(path) if os.path.exists(path) else 0,
        )
    )
    for path in candidates:
        try:
            con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
            try:
                found = con.execute(
                    "select 1 from sqlite_master "
                    "where type='table' and name='session_turn_capture_receipts'"
                ).fetchone()
                if found:
                    return path
            finally:
                con.close()
        except Exception:
            continue
    return ""


def _canonical_capture_snapshot(session_dir):
    result = {
        "available": False,
        "total": 0,
        "by_state": {},
        "materialized": 0,
        "error_present": False,
    }
    db_path = _find_session_database(session_dir)
    if not db_path:
        return result
    try:
        con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
        try:
            grouped = _group_counts(con, "session_turn_capture_receipts", "state")
            result["available"] = grouped["available"]
            result["total"] = grouped["total"]
            result["by_state"] = grouped["by_state"]
            result["materialized"] = result["by_state"].get("materialized", 0)
            result["error_present"] = grouped["error_present"]
        finally:
            con.close()
    except Exception:
        result["error_present"] = True
    return result


def memory_snapshot(db_path, session_dir):
    snapshot = {
        "exists": os.path.exists(db_path),
        "schema_version": 0,
        "canonical_capture_receipts": _canonical_capture_snapshot(session_dir),
        "digests": _empty_counts("status"),
        "digest_rollups": _empty_counts("rollup_status"),
        "jobs": _empty_counts("kind", "status"),
        "entries": _empty_counts("status", "kind"),
        "sources": _empty_counts("source_kind"),
        "windows": _empty_counts("status"),
        "summaries": _empty_counts("status", "freshness_status"),
        "capture_chain": {
            "available": False,
            "capture_backed_digests": 0,
            "phase1_v2_succeeded": 0,
            "phase1_legacy_succeeded": 0,
            "output_receipts": 0,
            "fully_linked": 0,
            "capture_backed_sources": 0,
            "capture_backed_entries": 0,
            "capture_backed_windows": 0,
            "capture_backed_window_summaries": 0,
            "error_present": False,
        },
    }
    if not snapshot["exists"]:
        return snapshot
    try:
        con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    except Exception:
        snapshot["error_present"] = True
        return snapshot
    try:
        try:
            snapshot["schema_version"] = con.execute(
                "select coalesce(max(version),0) from schema_migrations"
            ).fetchone()[0]
        except Exception:
            snapshot["schema_version_error_present"] = True

        snapshot["digests"] = _group_counts(con, "memory_turn_digests", "status")
        snapshot["digest_rollups"] = _group_counts(
            con, "memory_turn_digests", "rollup_status"
        )
        snapshot["jobs"] = _group_counts(con, "memory_jobs", "kind", "status")
        snapshot["entries"] = _group_counts(
            con, "memory_entries", "status", "kind"
        )
        snapshot["sources"] = _group_counts(con, "memory_sources", "source_kind")
        snapshot["windows"] = _group_counts(con, "memory_turn_windows", "status")
        snapshot["summaries"] = _group_counts(
            con, "memory_summaries", "status", "freshness_status"
        )

        chain = snapshot["capture_chain"]
        chain_queries = (
            (
                "capture_backed_digests",
                "select count(*) from memory_turn_digests "
                "where coalesce(capture_receipt_id, '') <> ''",
            ),
            (
                "phase1_v2_succeeded",
                "select count(*) from memory_turn_digests d "
                "join memory_jobs j on j.id = d.process_job_id "
                "where coalesce(d.capture_receipt_id, '') <> '' "
                "  and d.status = 'done' "
                "  and j.kind = 'phase1_extract/v2' and j.status = 'succeeded'",
            ),
            (
                "phase1_legacy_succeeded",
                "select count(*) from memory_jobs "
                "where kind = 'phase1_extract' and status = 'succeeded'",
            ),
            (
                "output_receipts",
                "select count(*) from memory_turn_output_receipts o "
                "join memory_turn_digests d on d.id = o.digest_id "
                "join memory_jobs j on j.id = o.job_id "
                "where coalesce(o.capture_receipt_id, '') <> '' "
                "  and o.project_key = d.project_key "
                "  and o.capture_receipt_id = d.capture_receipt_id "
                "  and o.job_id = d.process_job_id "
                "  and d.status = 'done' "
                "  and j.kind = 'phase1_extract/v2' and j.status = 'succeeded'",
            ),
            (
                "fully_linked",
                "select count(*) from memory_turn_digests d "
                "join memory_jobs j on j.id = d.process_job_id "
                "join memory_turn_output_receipts o "
                "  on o.project_key = d.project_key "
                " and o.capture_receipt_id = d.capture_receipt_id "
                " and o.digest_id = d.id "
                " and o.job_id = j.id "
                "where coalesce(d.capture_receipt_id, '') <> '' "
                "  and d.status = 'done' "
                "  and j.kind = 'phase1_extract/v2' "
                "  and j.status = 'succeeded'",
            ),
            (
                "capture_backed_sources",
                "select count(*) from memory_sources s "
                "join memory_entries e on e.id = s.memory_id "
                "join memory_turn_digests d "
                "  on d.session_id = s.session_id and d.turn_id = s.turn_id "
                " and d.session_generation = s.source_session_generation "
                " and d.source_revision = s.source_revision "
                " and d.range_hash = s.source_hash and d.project_key = e.project_key "
                "join memory_jobs j on j.id = d.process_job_id "
                "join memory_turn_output_receipts o "
                "  on o.digest_id = d.id and o.job_id = j.id "
                " and o.capture_receipt_id = d.capture_receipt_id "
                "where coalesce(d.capture_receipt_id, '') <> '' "
                "  and d.status = 'done' and j.kind = 'phase1_extract/v2' "
                "  and j.status = 'succeeded' "
                "  and s.source_kind = 'session_extract' "
                "  and e.source_kind = 'session_extract' "
                "  and e.status in ('candidate', 'active') "
                "  and coalesce(e.deleted_at, '') = ''",
            ),
            (
                "capture_backed_entries",
                "select count(distinct e.id) from memory_sources s "
                "join memory_entries e on e.id = s.memory_id "
                "join memory_turn_digests d "
                "  on d.session_id = s.session_id and d.turn_id = s.turn_id "
                " and d.session_generation = s.source_session_generation "
                " and d.source_revision = s.source_revision "
                " and d.range_hash = s.source_hash and d.project_key = e.project_key "
                "join memory_jobs j on j.id = d.process_job_id "
                "join memory_turn_output_receipts o "
                "  on o.digest_id = d.id and o.job_id = j.id "
                " and o.capture_receipt_id = d.capture_receipt_id "
                "where coalesce(d.capture_receipt_id, '') <> '' "
                "  and d.status = 'done' and j.kind = 'phase1_extract/v2' "
                "  and j.status = 'succeeded' "
                "  and s.source_kind = 'session_extract' "
                "  and e.source_kind = 'session_extract' "
                "  and e.status in ('candidate', 'active') "
                "  and coalesce(e.deleted_at, '') = ''",
            ),
            (
                "capture_backed_windows",
                "select count(distinct w.window_seq) "
                "from memory_turn_windows w "
                "where w.status = 'done' and exists ("
                "  select 1 from memory_turn_digests d "
                "  where d.project_key = w.project_key "
                "    and d.session_id = w.session_id "
                "    and d.session_generation = w.session_generation "
                "    and d.digest_seq between w.start_digest_seq and w.end_digest_seq "
                "    and coalesce(d.capture_receipt_id, '') <> '' "
                "    and d.status = 'done' and d.rollup_status = 'done' "
                "    and d.rollup_window_id = w.id"
                ")",
            ),
            (
                "capture_backed_window_summaries",
                "select count(distinct w.window_seq) "
                "from memory_turn_windows w "
                "where w.status = 'done' and trim(w.summary_text) <> '' and exists ("
                "  select 1 from memory_turn_digests d "
                "  where d.project_key = w.project_key "
                "    and d.session_id = w.session_id "
                "    and d.session_generation = w.session_generation "
                "    and d.digest_seq between w.start_digest_seq and w.end_digest_seq "
                "    and coalesce(d.capture_receipt_id, '') <> '' "
                "    and d.status = 'done' and d.rollup_status = 'done' "
                "    and d.rollup_window_id = w.id"
                ")",
            ),
        )
        chain["available"] = True
        for key, query in chain_queries:
            value, failed = _count_query(con, query)
            chain[key] = value
            chain["error_present"] = chain["error_present"] or failed
        if chain["error_present"]:
            chain["available"] = False
    finally:
        con.close()
    return snapshot


def capture_chain_ready(snapshot):
    receipt = snapshot["canonical_capture_receipts"]
    chain = snapshot["capture_chain"]
    return (
        receipt["available"]
        and receipt["materialized"] > 0
        and chain["available"]
        and chain["capture_backed_digests"] > 0
        and chain["phase1_v2_succeeded"] > 0
        and chain["output_receipts"] > 0
        and chain["fully_linked"] > 0
        and chain["capture_backed_sources"] > 0
        and chain["capture_backed_entries"] > 0
        and chain["capture_backed_windows"] > 0
        and chain["capture_backed_window_summaries"] > 0
    )


def wait_for_capture_chain(db_path, session_dir, timeout):
    deadline = time.monotonic() + timeout
    snapshot = memory_snapshot(db_path, session_dir)
    while not capture_chain_ready(snapshot) and time.monotonic() < deadline:
        time.sleep(0.5)
        snapshot = memory_snapshot(db_path, session_dir)
    return snapshot


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--exe",
        default=os.environ.get("AICLI_MEMORY_CONPTY_EXE", ""),
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
    if not args.exe or not os.path.isfile(args.exe):
        print("[FAIL] provide --exe pointing to a freshly built executable")
        return 2

    out_dir = os.path.join(
        r"E:\projects\ai\ai-agent-runtime\output\memory-conpty",
        time.strftime("%Y%m%d-%H%M%S"),
    )
    os.makedirs(out_dir, exist_ok=True)
    workspace = tempfile.mkdtemp(prefix="aicli-memory-conpty-ws-")
    session_dir = tempfile.mkdtemp(prefix="aicli-memory-conpty-sessions-")
    log_dir = tempfile.mkdtemp(prefix="aicli-memory-conpty-logs-")
    # Session/log artifacts may contain prompts, model output, or provider
    # request bodies. They are only needed while this run is executing.
    atexit.register(shutil.rmtree, workspace, ignore_errors=True)
    atexit.register(shutil.rmtree, session_dir, ignore_errors=True)
    atexit.register(shutil.rmtree, log_dir, ignore_errors=True)
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
      windowSummaryEnabled: true
      windowTurns: 1
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
    print("EXE: configured")
    print("MODEL:", args.provider, args.model, args.reasoning)

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
    snapshot = wait_for_capture_chain(
        db_path, session_dir, max(0.0, min(args.timeout, 60.0))
    )
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
            "session_found": bool(recall_session_dir),
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

        snapshot = wait_for_capture_chain(
            db_path, session_dir, max(0.0, min(args.timeout, 60.0))
        )
        with open(
            os.path.join(out_dir, "memory-snapshot.json"),
            "w",
            encoding="utf-8",
        ) as fh:
            json.dump(snapshot, fh, ensure_ascii=False, indent=2)

    receipt = snapshot["canonical_capture_receipts"]
    chain = snapshot["capture_chain"]
    digest_total = snapshot["digests"]["total"]
    entries_total = snapshot["entries"]["total"]
    checks = [
        ("real provider turn finished in ConPTY", turn_finished_seen),
        ("first session finalized before commands", finalized_seen),
        ("first session exited cleanly", clean_exit),
        ("real provider turn reached ConPTY marker", marker_seen),
        ("/memory status rendered runtime/digest diagnostics", status_seen),
        ("turn digest rows were persisted", digest_total > 0),
        ("canonical capture receipt exists", receipt["available"] and receipt["total"] > 0),
        ("canonical capture receipt was materialized", receipt["materialized"] > 0),
        ("capture-chain evidence queries completed", chain["available"] and not chain["error_present"]),
        ("capture-backed turn digest exists", chain["capture_backed_digests"] > 0),
        (
            "capture-backed phase1_extract/v2 succeeded (legacy ignored)",
            chain["phase1_v2_succeeded"] > 0,
        ),
        (
            "legacy phase1 is not being used as capture proof",
            chain["phase1_v2_succeeded"] > 0,
        ),
        ("capture-backed turn output receipt exists", chain["output_receipts"] > 0),
        ("digest/job/output receipt are fully linked", chain["fully_linked"] > 0),
        ("capture-backed source/provenance exists", chain["capture_backed_sources"] > 0),
        ("capture-backed memory entry/candidate exists", chain["capture_backed_entries"] > 0),
        ("capture-backed turn window completed", chain["capture_backed_windows"] > 0),
        ("capture-backed turn window summary exists", chain["capture_backed_window_summaries"] > 0),
        ("memory entries were persisted", entries_total > 0),
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
    print("DIGESTS:", json.dumps(snapshot["digests"], ensure_ascii=False))
    print("JOBS:", json.dumps(snapshot["jobs"], ensure_ascii=False))
    print("ENTRIES:", json.dumps(snapshot["entries"], ensure_ascii=False))
    print("SOURCES:", json.dumps(snapshot["sources"], ensure_ascii=False))
    print("WINDOWS:", json.dumps(snapshot["windows"], ensure_ascii=False))
    print("SUMMARIES:", json.dumps(snapshot["summaries"], ensure_ascii=False))
    print("CAPTURE_CHAIN:", json.dumps(chain, ensure_ascii=False))
    print("ARTIFACTS:", os.path.basename(out_dir))
    print(("RESULT: PASS" if not failed else "RESULT: FAIL") + f" failed={failed}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
