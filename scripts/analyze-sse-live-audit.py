#!/usr/bin/env python3
"""SSE live 通道成本审计：session_events 的行数/字节按类型分布 + 双写抽样对照。

只读：以 file: URI 的 ``mode=ro`` 打开运行时库，不写任何数据。

迁移自取证期草稿 ``.tmp/sse-live-audit.py``（该目录被 ``.gitignore`` 忽略、仓库内
不可复跑，见方案文档附录 A 的落点说明）。迁移时按要求保留 ``--db`` 参数与只读
模式，并把硬编码路径改为「相对仓库根解析的默认值」。

用法：
    python scripts/analyze-sse-live-audit.py
    python scripts/analyze-sse-live-audit.py --db backend/data/runtime/session_runtime.sqlite
    python scripts/analyze-sse-live-audit.py --session session_20260916133052_DktyYFb3
"""

from __future__ import annotations

import argparse
import sqlite3
import sys
from pathlib import Path


def parse_args(argv: list[str]) -> argparse.Namespace:
    repo_root = Path(__file__).resolve().parents[1]
    default_db = repo_root / "backend" / "data" / "runtime" / "session_runtime.sqlite"
    parser = argparse.ArgumentParser(
        description="session_events 行数/字节按类型分布审计（只读）",
    )
    parser.add_argument(
        "--db",
        default=str(default_db),
        help="session_runtime.sqlite 路径（默认相对仓库根解析；只读打开）",
    )
    parser.add_argument(
        "--session",
        default="",
        help="样本会话 id（默认取行数最多的会话）",
    )
    parser.add_argument(
        "--top-sessions",
        type=int,
        default=8,
        dest="top_sessions",
        help="按行数排行的会话条数（默认 8）",
    )
    parser.add_argument(
        "--global-limit",
        type=int,
        default=15,
        dest="global_limit",
        help="全库类型分布 Top-N（默认 15）",
    )
    return parser.parse_args(argv)


def open_read_only(db_path: str) -> sqlite3.Connection:
    path = Path(db_path)
    if not path.exists():
        sys.exit(f"db not found: {path}")
    uri = "file:" + path.as_posix() + "?mode=ro"
    return sqlite3.connect(uri, uri=True)


def print_session_ranking(cur: sqlite3.Cursor, limit: int) -> None:
    print(f"\n=== top {limit} sessions by rows ===")
    for sid, n in cur.execute(
        "select session_id, count(*) c from session_events"
        " group by session_id order by c desc limit ?",
        (limit,),
    ):
        print(f"{n:>7}  {sid}")


def resolve_sample_session(cur: sqlite3.Cursor, requested: str) -> str:
    if requested.strip():
        return requested.strip()
    row = cur.execute(
        "select session_id from session_events group by session_id"
        " order by count(*) desc limit 1"
    ).fetchone()
    if row is None:
        sys.exit("session_events is empty")
    return row[0]


def print_session_breakdown(cur: sqlite3.Cursor, session_id: str) -> int:
    print(f"\n=== type breakdown for top session {session_id} ===")
    rows = cur.execute(
        """
        select type, count(*) c, sum(length(cast(payload_json as blob))) b
        from session_events where session_id = ? group by type order by b desc
        """,
        (session_id,),
    ).fetchall()
    total_bytes = sum(row[2] or 0 for row in rows)
    for event_type, count, size in rows:
        share = 100.0 * (size or 0) / max(total_bytes, 1)
        print(
            f"{event_type:<34} rows={count:<6} bytes={size:>9}  ({share:5.1f}% of bytes)"
        )
    print(f"session payload bytes total = {total_bytes}")
    return total_bytes


def print_global_distribution(cur: sqlite3.Cursor, limit: int) -> None:
    print("\n=== global type distribution (all sessions, by bytes) ===")
    rows = cur.execute(
        """
        select type, count(*) c, sum(length(cast(payload_json as blob))) b
        from session_events group by type order by b desc limit ?
        """,
        (limit,),
    ).fetchall()
    global_bytes = (
        cur.execute(
            "select sum(length(cast(payload_json as blob))) from session_events"
        ).fetchone()[0]
        or 0
    )
    print(f"global payload bytes = {global_bytes}")
    for event_type, count, size in rows:
        share = 100.0 * (size or 0) / max(global_bytes, 1)
        print(
            f"{event_type:<34} rows={count:<7} bytes={size:>10}  ({share:5.1f}%)"
        )


def print_duplicate_write_sample(cur: sqlite3.Cursor, session_id: str) -> None:
    print("\n=== duplicate-write sample (reasoning: bus row vs chat.sse row) ===")
    bus = cur.execute(
        "select seq, payload_json from session_events"
        " where session_id=? and type='assistant.reasoning' order by seq desc limit 2",
        (session_id,),
    ).fetchall()
    sse = cur.execute(
        "select seq, payload_json from session_events"
        " where session_id=? and type='chat.sse.reasoning' order by seq desc limit 2",
        (session_id,),
    ).fetchall()
    for label, rows in (("assistant.reasoning", bus), ("chat.sse.reasoning", sse)):
        for seq, payload in rows:
            print(
                f"{label:<22} seq={seq:<7} len={len(payload):<5} head={payload[:110]!r}"
            )


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    con = open_read_only(args.db)
    try:
        cur = con.cursor()
        columns = [row[1] for row in cur.execute("pragma table_info(session_events)")]
        print("session_events columns:", columns)
        print("total rows:", cur.execute("select count(*) from session_events").fetchone()[0])
        print_session_ranking(cur, args.top_sessions)
        session_id = resolve_sample_session(cur, args.session)
        if args.session.strip():
            print(f"\n=== sample session (explicit) {session_id} ===")
        print_session_breakdown(cur, session_id)
        print_global_distribution(cur, args.global_limit)
        print_duplicate_write_sample(cur, session_id)
    finally:
        con.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
