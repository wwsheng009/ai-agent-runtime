"""ConPTY-driven real-terminal test for the startup provider picker.

pywinpty's pty.read() is BLOCKING, so a background thread accumulates output
while the main thread waits for patterns with deadlines. This keeps the probe
hanging-proof: every wait has a hard timeout and a global watchdog kills the
run.

Scenarios:
  1. Empty isolated HOME -> startup picker shows the create row first
     (fresh-install dead-end "没有可用的 providers" is gone).
  2. Confirming the create row opens the login flow's "新建方式" picker
     listing the builtin opencode-gateway template.
  3. Choosing custom (row 2) reaches the legacy provider-name prompt.
"""
import os
import sys
import threading
import time
import tempfile

from winpty import PtyProcess

EXE = r"E:\projects\ai\ai-agent-runtime\backend\dist\aicli-test.exe"
SEND_ENTER = "\r"
SEND_ESC = "\x1b"

HARD_DEADLINE = time.time() + 90


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
    """Cheap VT scrub for pattern matching."""
    out = []
    i = 0
    n = len(text)
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


def wait_for(rec: PtyRecorder, *patterns, deadline=15.0) -> tuple[bool, str]:
    """Poll accumulated visible output until any pattern shows up."""
    end = min(time.time() + deadline, HARD_DEADLINE)
    while time.time() < end:
        text = visible(rec.text())
        for pat in patterns:
            if pat in text:
                return True, text
        time.sleep(0.2)
    return False, visible(rec.text())


def wait_for_new(rec: PtyRecorder, *patterns, since_len=0, deadline=15.0) -> tuple[bool, str]:
    """Like wait_for but only matches output produced after `since_len`
    characters — guards against matching stale screens."""
    end = min(time.time() + deadline, HARD_DEADLINE)
    while time.time() < end:
        text = visible(rec.text())
        suffix = text[since_len:]
        for pat in patterns:
            if pat in suffix:
                return True, suffix
        time.sleep(0.2)
    return False, visible(rec.text())[since_len:]


def main() -> int:
    sys.stdout.reconfigure(line_buffering=True)
    home = tempfile.mkdtemp(prefix="aicli-conpty-home-")
    os.environ["USERPROFILE"] = home
    os.environ["HOME"] = home
    workdir = tempfile.mkdtemp(prefix="aicli-conpty-cwd-")
    failures = []

    def check(name, cond, evidence=""):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append((name, evidence))

    # ---- Scenario 1: empty HOME -> startup picker with create row ----------
    pty = PtyProcess.spawn(
        [EXE, "chat"],
        cwd=workdir,
        dimensions=(40, 120),
        env={**os.environ},
    )
    rec = PtyRecorder(pty)

    ok, out1 = wait_for(rec, "选择 Provider", "新建 provider", deadline=25.0)
    check("scenario1: startup picker appeared", ok, out1[-3000:])
    check("scenario1: create row present", "新建 provider" in out1, out1[-3000:])
    check(
        "scenario1: legacy dead-end gone",
        "没有可用的 providers" not in out1,
        out1[-3000:],
    )

    # ---- Scenario 2: Enter on create row -> login -> 新建方式 picker -------
    pty.write(SEND_ENTER)
    ok, out2 = wait_for(rec, "新建方式", "选择新建方式", deadline=20.0)
    check("scenario2: template/custom picker appeared", ok, out2[-3000:])
    check(
        "scenario2: builtin opencode template listed",
        "opencode.ai" in out2,
        out2[-3000:],
    )

    # ---- Scenario 3: custom path -> login provider picker ------------------
    # The full-screen picker filters-as-you-type, so digits would be treated
    # as search terms. Navigate: ↓ moves from row 1 (template) to row 2
    # (custom), then Enter confirms. The custom path lands in the login
    # provider picker whose first row is "＋ 新建 provider（手动输入名称）".
    pty.write("\x1b[B")  # down arrow
    time.sleep(0.5)
    pty.write(SEND_ENTER)
    ok, out3 = wait_for(
        rec,
        "手动输入名称",
        deadline=15.0,
    )
    check("scenario3: login provider picker reached", ok, out3[-3000:])
    check(
        "scenario3: create row present in login picker",
        "手动输入名称" in out3,
        out3[-3000:],
    )

    # ---- Scenario 4: template path end-to-end ------------------------------
    # Cancel back to the template picker is unreliable across stacked
    # pickers; instead spawn a fresh session and take the template branch:
    # Enter on create row -> Enter on template row -> API key prompt.
    try:
        pty.write("\x03")
        time.sleep(0.8)
        pty.terminate(force=True)
    except Exception:
        pass

    pty2 = PtyProcess.spawn(
        [EXE, "chat"],
        cwd=workdir,
        dimensions=(40, 120),
        env={**os.environ},
    )
    rec2 = PtyRecorder(pty2)
    ok, _ = wait_for(rec2, "选择 Provider", deadline=25.0)
    if not ok:
        check("scenario4: fresh session picker", False, visible(rec2.text())[-2000:])
    else:
        rec2.pty.write(SEND_ENTER)  # create row
        ok, out4 = wait_for(rec2, "新建方式", deadline=15.0)
        if not ok:
            check("scenario4: template picker", False, out4[-2000:])
        else:
            since = len(visible(rec2.text()))
            rec2.pty.write(SEND_ENTER)  # row 1 = builtin template
            ok, out4b = wait_for_new(
                rec2,
                "API key", "api key", "凭据", "Base URL",
                # The builtin template name collides with the preset-injected
                # provider, so the login flow may treat it as an update and
                # skip straight to confirmation; accept either the credential
                # prompt or the models-validation notice.
                "校验", "default_model", "模型",
                since_len=since,
                deadline=20.0,
            )
            check(
                "scenario4: template path reaches credential/validation step",
                ok,
                out4b[-3000:],
            )
    try:
        rec2.pty.write("\x03")
        time.sleep(0.6)
        rec2.pty.terminate(force=True)
    except Exception:
        pass

    # Clean exit: interrupt then terminate.
    try:
        pty.write("\x03")
        time.sleep(0.5)
        pty.terminate(force=True)
    except Exception:
        pass

    # Dump artifacts for debugging.
    log_path = os.path.join(os.path.dirname(EXE), "conpty-run.log")
    with open(log_path, "w", encoding="utf-8", errors="replace") as fh:
        fh.write("=== HOME: " + home + "\n")
        fh.write("=== CWD: " + workdir + "\n")
        fh.write("=== FULL VISIBLE OUTPUT ===\n")
        fh.write(visible(rec.text()))

    print()
    print(f"HOME isolation: {home}")
    print(f"CWD: {workdir}")
    print(f"full transcript: {log_path}")
    if failures:
        print(f"\n{len(failures)} failure(s)")
        for name, ev in failures:
            print(f"--- {name} ---")
            print(ev[-1200:])
        return 1
    print("\nAll scenario checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
