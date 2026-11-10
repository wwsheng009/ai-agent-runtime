// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TFunction } from "i18next";

import { MemoryV2Panel } from "./memory-v2-panel";

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

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    globalThis.fetch = originalFetch;
  });

  it("renders status, summaries, records, usage and triggers rebuild", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
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
      return jsonResponse({});
    });
    globalThis.fetch = fetchMock as typeof fetch;

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
  });
});
