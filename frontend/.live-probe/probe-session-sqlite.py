"""临时探针：直读 runtime SQLite，核对 A 会话回合的开始/结束时间（收尾需删除）。"""

import json
import sqlite3
import sys

DB = r"E:\projects\ai\ai-agent-runtime\backend\data\runtime\session_runtime.sqlite"
SID = sys.argv[1] if len(sys.argv) > 1 else "session_20260917101949_6RiokSno"

con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
con.row_factory = sqlite3.Row

tables = [r["name"] for r in con.execute("select name from sqlite_master where type='table'")]
print("TABLES:", tables)

for table in tables:
    cols = [r["name"] for r in con.execute(f"pragma table_info({table})")]
    print(f"\n== {table} cols={cols}")
    if not any(c in cols for c in ("session_id", "session", "sessionId", "id")):
        continue
    key = next((c for c in ("session_id", "session", "sessionId") if c in cols), None)
    timecols = [c for c in cols if "time" in c.lower() or c.lower() in ("at", "ts", "created_at", "updated_at")]
    try:
        if key:
            rows = list(con.execute(f"select * from {table} where {key}=?", (SID,)))
        else:
            rows = []
        print(f"rows_for_sid={len(rows)}")
        for row in rows[:12]:
            data = {k: row[k] for k in row.keys() if k not in ("payload", "data", "content")}
            print("  ", json.dumps(data, ensure_ascii=False, default=str)[:600])
        if timecols:
            if key:
                agg = con.execute(
                    f"select min({timecols[0]}) mn, max({timecols[0]}) mx, count(*) n "
                    f"from {table} where {key}=?",
                    (SID,),
                ).fetchone()
                print(f"   time[0]={timecols[0]} min={agg['mn']} max={agg['mx']} n={agg['n']}")
            else:
                agg = con.execute(
                    f"select min({timecols[0]}) mn, max({timecols[0]}) mx, count(*) n from {table}"
                ).fetchone()
                print(f"   time[0]={timecols[0]} min={agg['mn']} max={agg['mx']} n={agg['n']}")
    except sqlite3.Error as exc:
        print("   ERR", exc)
