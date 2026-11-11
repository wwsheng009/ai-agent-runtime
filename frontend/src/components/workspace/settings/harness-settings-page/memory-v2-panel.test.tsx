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
    if (key === "harness.memoryUseCount") return `use ${options?.count}`;
    if (key === "harness.memoryRebuildSummary") return "Rebuild summary";
    if (key === "harness.memoryExport") return "Export";
    if (key === "harness.memoryRevalidate") return "Revalidate";
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
          fallback: false,
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
      if (url.includes("/memory/summaries/rebuild")) {
        return jsonResponse({ workspace_path: "/tmp/w", rebuilt: 1, revision: 3 });
      }
      if (url.includes("/memory/summaries")) {
        return jsonResponse({
          workspace_path: "/tmp/w", count: 1,
          items: [{
            id: "sum_1", scope: "project", project_key: "pk", summary_kind: "long_term",
            revision: 2, status: "active", freshness_status: "fresh",
            text: "Stable summary.", token_estimate: 12, source_count: 1,
            generated_at: "2026-11-10T00:00:00Z",
          }],
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
    expect(container.textContent).toContain("Stable summary.");
    expect(container.textContent).toContain("Use append-only memory.");
    expect(container.textContent).toContain("injected_only");
    expect(container.textContent).toContain("rebuild boom");
    expect(container.textContent).toContain("queryTrace");

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
  });
});
