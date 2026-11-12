// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TFunction } from "i18next";

import { MemoryV2Panel } from "./memory-v2-panel";
import { adminTokenStorageKey } from "@/lib/admin-token";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function translator(): TFunction<"settings"> {
  return ((key: string, options?: Record<string, unknown>) => {
    if (key === "harness.memoryRevision") return `r${options?.revision}`;
    if (key === "harness.memorySummaryMeta") {
      return `r${options?.revision} ${options?.tokens} ${options?.items}`;
    }
    if (key === "harness.memoryTurnDigests") return "Turn digests";
    if (key === "harness.memoryTurnWindows") return "Turn windows";
    if (key === "harness.memoryPendingDigestAge") {
      return `Oldest pending: ${options?.seconds}s`;
    }
    if (key === "harness.memoryPendingDigestSessions") {
      return `Pending sessions: ${options?.count}`;
    }
    if (key === "harness.memoryBacklogBatches") {
      return `Backlog batches: ${options?.count}`;
    }
    if (key === "harness.memoryRecoveredSessions") {
      return `Recovered sessions: ${options?.count}`;
    }
    if (key === "harness.memoryActivateSummary") return "Activate revision";
    if (key === "harness.memorySessionGovernance") return "Session governance";
    if (key === "harness.memorySessionIDPlaceholder") return "Session ID";
    if (key === "harness.memorySessionMode") return "Session mode";
    if (key === "harness.memorySessionModeEnabled") return "enabled";
    if (key === "harness.memorySessionModeDisabled") return "disabled";
    if (key === "harness.memorySessionModePolluted") return "polluted";
    if (key === "harness.memorySessionModeApply") return "Apply session mode";
    if (key === "harness.memoryPurgeSessionWindows") return "Purge session L1";
    if (key === "harness.memoryPurgeAllWindows") return "Purge all L1";
    if (key === "harness.memoryUseCount") return `use ${options?.count}`;
    if (key === "harness.memoryRebuildSummary") return "Rebuild summary";
    if (key === "harness.memoryExport") return "Export";
    if (key === "harness.memoryRevalidate") return "Revalidate";
    if (key === "harness.memoryRestore") return "Restore";
    if (key === "harness.memoryReset") return "Reset all";
    if (key === "harness.memoryWhyNotRun") return "Explain recall";
    if (key === "harness.memoryUnpin") return "Unpin";
    if (key === "harness.memoryPin") return "Pin";
    if (key === "harness.memoryAutoLearningEnabled") return `Auto ${options?.trigger}`;
    if (key === "harness.memoryCorrect") return "Correct";
    if (key === "harness.memoryForget") return "Forget";
    if (key === "harness.refresh") return "Refresh";
    if (key === "harness.memoryV2Title") return "Memory v2 status";
    if (key === "harness.memoryV2Description") return "description";
    if (key === "harness.memoryV2Empty") return "empty";
    return key.replace("harness.", "");
  }) as TFunction<"settings">;
}

describe("MemoryV2Panel", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const originalFetch = globalThis.fetch;
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;
  const originalAnchorClick = HTMLAnchorElement.prototype.click;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    URL.createObjectURL = vi.fn(() => "blob:memory-export");
    URL.revokeObjectURL = vi.fn();
    HTMLAnchorElement.prototype.click = vi.fn();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    globalThis.fetch = originalFetch;
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
    HTMLAnchorElement.prototype.click = originalAnchorClick;
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("renders status, summaries, records, usage and triggers rebuild", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      void init;
      const url = String(input);
      if (url.includes("/memory/status")) {
        return jsonResponse({
          workspace_path: "/tmp/w", mode: "on", scope: "project",
          schema_version: 3, total: 1, summary_revision: 2,
          summary_status: "active", summary_freshness: "fresh",
          pinned_count: 1, pinned_budget_ratio: 0.2, injection_mode: "hybrid",
          generation_enabled: true, generation_trigger: "both",
          consolidation_enabled: true, manual_enabled: true,
          turn_digests: { pending: 2, done: 1 },
          turn_windows: { done: 1 },
          oldest_pending_digest_age_seconds: 120,
          pending_digest_sessions: 2,
          pending_digest_backlog_batches: 1,
          recovered_digest_sessions: 3,
          fallback: false,
        });
      }
      if (url.includes("/memory/records") && url.includes("status=deleted")) {
        return jsonResponse({
          workspace_path: "/tmp/w", count: 1,
          items: [{
            id: "mem_deleted", scope: "project", kind: "decision", status: "deleted",
            text: "Deleted memory.", pinned: false, source_kind: "manual",
            revision: 2, freshness_status: "fresh", use_count: 0,
          }],
        });
      }
      if (url.includes("/memory/records")) {
        return jsonResponse({
          workspace_path: "/tmp/w", count: 1,
          items: [{
            id: "mem_1", scope: "project", kind: "decision", status: "active",
            text: "Use append-only memory.", pinned: true, source_kind: "manual",
            revision: 1, freshness_status: "fresh", use_count: 2,
          }],
        });
      }
      if (url.includes("/memory/why-not")) {
        return jsonResponse({
          workspace_path: "/tmp/w", mode: "on", query_preview: "alpha",
          query_truncated: false, candidate_count: 2, filtered_count: 1,
          selected: [{ id: "mem_1", stage: "stable", kind: "decision", pinned: true }],
          dropped: [{ id: "mem_drop", reason: "no_match" }],
          token_estimate: 12, token_budget: 600, injected: true,
        });
      }
      if (url.includes("/memory/records/mem_deleted/restore")) {
        return jsonResponse({ id: "mem_deleted", status: "active" });
      }
      if (url.includes("/memory/reset")) {
        return jsonResponse({ ok: true, action: "reset", workspace_path: "/tmp/w" });
      }
      if (url.includes("/memory/summaries/rebuild")) {
        return jsonResponse({ workspace_path: "/tmp/w", rebuilt: 1, revision: 3 });
      }
      if (url.includes("/memory/summaries/sum_old/activate")) {
        return jsonResponse({
          id: "sum_old", revision: 1, status: "active", freshness_status: "fresh",
        });
      }
      if (url.includes("/memory/summaries")) {
        return jsonResponse({
          workspace_path: "/tmp/w", count: 2,
          items: [
            {
              id: "sum_1", scope: "project", project_key: "pk", summary_kind: "long_term",
              revision: 2, status: "active", freshness_status: "fresh",
              text: "Stable summary.", token_estimate: 12, source_count: 1,
              generated_at: "2026-11-10T00:00:00Z",
            },
            {
              id: "sum_old", scope: "project", project_key: "pk", summary_kind: "long_term",
              revision: 1, status: "superseded", freshness_status: "fresh",
              text: "Old summary.", token_estimate: 10, source_count: 1,
              generated_at: "2026-11-09T00:00:00Z",
            },
          ],
        });
      }
      if (url.includes("/memory/sessions/s1/mode")) {
        return jsonResponse({ ok: true, session_id: "s1", mode: "enabled" });
      }
      if (url.includes("/memory/turn-windows/purge")) {
        return jsonResponse({
          ok: true, action: "purge_turn_windows", workspace_path: "/tmp/w",
          scope: "project", windows_deleted: 1, digests_released: 1,
        });
      }
      if (url.includes("/memory/usage")) {
        return jsonResponse({
          workspace_path: "/tmp/w", count: 1,
          items: [{
            id: "usage_1", memory_id: "mem_1", session_id: "s1",
            used_at: "2026-11-10T00:00:00Z", use_kind: "injected_only",
          }],
        });
      }
      if (url.includes("/memory/jobs")) {
        return jsonResponse({
          items: [{
            id: "job_1", kind: "summary_rebuild", job_key: "sum_1:wm", scope: "project",
            project_key: "pk", status: "failed", priority: 0, attempt: 3, max_attempts: 3,
            last_error: "rebuild boom", created_at: "2026-11-10T00:00:00Z",
            updated_at: "2026-11-10T00:00:00Z",
          }],
          count: 1,
        });
      }
      if (url.includes("/memory/recall-traces")) {
        return jsonResponse({
          items: [{
            id: "trace_1", session_id: "s1", project_key: "pk", query_hash: "hash",
            query_preview: "queryTrace", candidate_count: 2, filtered_count: 1,
            dropped: [{ id: "mem_drop", reason: "memory_budget" }],
            token_estimate: 32, token_budget: 100, injected: true,
            created_at: "2026-11-10T00:00:00Z",
          }],
          count: 1,
        });
      }
      if (url.includes("/memory/export")) {
        return new Response("# memory export", {
          status: 200,
          headers: { "Content-Type": "text/markdown" },
        });
      }
      return jsonResponse({});
    });
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem(adminTokenStorageKey, "memory-admin-token");

    await act(async () => {
      root?.render(<MemoryV2Panel t={translator()} workspacePath="/tmp/w" />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain("hybrid");
    expect(container.textContent).toContain("Auto both");
    expect(container.textContent).toContain("Stable summary.");
    expect(container.textContent).toContain("Use append-only memory.");
    expect(container.textContent).toContain("injected_only");
    expect(container.textContent).toContain("rebuild boom");
    expect(container.textContent).toContain("queryTrace");
    expect(container.textContent).toContain("Deleted memory.");
    expect(container.textContent).toContain("pending=2");
    expect(container.textContent).toContain("done=1");
    expect(container.textContent).toContain("Oldest pending: 120s");

    const rebuildButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Rebuild summary"),
    );
    expect(rebuildButton).toBeTruthy();
    await act(async () => {
      rebuildButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/summaries/rebuild") &&
      (init?.method ?? "GET").toUpperCase() === "POST",
    )).toBe(true);

    const activateButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Activate revision"),
    );
    expect(activateButton).toBeTruthy();
    await act(async () => {
      activateButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/summaries/sum_old/activate") &&
      (init?.method ?? "GET").toUpperCase() === "POST",
    )).toBe(true);

    const sessionInput = container.querySelector('input[aria-label="Session ID"]') as HTMLInputElement | null;
    expect(sessionInput).toBeTruthy();
    await act(async () => {
      if (sessionInput) {
        const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
        setValue?.call(sessionInput, "s1");
        sessionInput.dispatchEvent(new Event("input", { bubbles: true }));
        sessionInput.dispatchEvent(new Event("change", { bubbles: true }));
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    });
    const applySessionModeButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Apply session mode"),
    );
    expect(applySessionModeButton).toBeTruthy();
    await act(async () => {
      applySessionModeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/sessions/s1/mode") &&
      (init?.method ?? "GET").toUpperCase() === "POST" &&
      String(init?.body).includes('"mode":"enabled"'),
    )).toBe(true);

    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const purgeAllButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Purge all L1"),
    );
    expect(purgeAllButton).toBeTruthy();
    await act(async () => {
      purgeAllButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/turn-windows/purge") &&
      (init?.method ?? "GET").toUpperCase() === "POST" &&
      String(init?.body).includes('"all":true'),
    )).toBe(true);

    const purgeSessionButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Purge session L1"),
    );
    expect(purgeSessionButton).toBeTruthy();
    await act(async () => {
      purgeSessionButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/turn-windows/purge") &&
      (init?.method ?? "GET").toUpperCase() === "POST" &&
      String(init?.body).includes('"session_id":"s1"'),
    )).toBe(true);

    const exportButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Export"),
    );
    expect(exportButton).toBeTruthy();
    await act(async () => {
      exportButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/export") &&
      (init?.headers as Record<string, string>)?.["X-Skills-Admin-Token"] === "memory-admin-token",
    )).toBe(true);
    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:memory-export");

    const pinButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Unpin"),
    );
    expect(pinButton).toBeTruthy();
    await act(async () => {
      pinButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/records/mem_1") &&
      (init?.method ?? "GET").toUpperCase() === "PATCH" &&
      String(init?.body).includes('"pinned":false'),
    )).toBe(true);

    const whyNotInput = container.querySelector('input[aria-label="memoryWhyNotPlaceholder"]') as HTMLInputElement | null;
    expect(whyNotInput).toBeTruthy();
    await act(async () => {
      if (whyNotInput) {
        const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
        setValue?.call(whyNotInput, "alpha");
        whyNotInput.dispatchEvent(new Event("input", { bubbles: true }));
        whyNotInput.dispatchEvent(new Event("change", { bubbles: true }));
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    });
    const whyNotButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Explain recall"),
    );
    expect(whyNotButton).toBeTruthy();
    expect((whyNotButton as HTMLButtonElement | undefined)?.disabled).toBe(false);
    await act(async () => {
      whyNotButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(fetchMock.mock.calls.some(([input]) =>
      String(input).includes("/memory/why-not") && String(input).includes("q=alpha"),
    )).toBe(true);
    expect(container.textContent).toContain("mem_drop: no_match");

    const restoreButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Restore"),
    );
    expect(restoreButton).toBeTruthy();
    await act(async () => {
      restoreButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/records/mem_deleted/restore") &&
      (init?.method ?? "GET").toUpperCase() === "POST",
    )).toBe(true);

    const resetButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("Reset all"),
    );
    expect(resetButton).toBeTruthy();
    await act(async () => {
      resetButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(confirmSpy).toHaveBeenCalled();
    expect(fetchMock.mock.calls.some(([input, init]) =>
      String(input).includes("/memory/reset") &&
      (init?.method ?? "GET").toUpperCase() === "POST" &&
      String(init?.body).includes('"confirm":true'),
    )).toBe(true);
  });
});
