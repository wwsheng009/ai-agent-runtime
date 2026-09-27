// @vitest-environment jsdom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { AnalyticsRouteEvent } from "@/types/runtime";
import { routeEventsResponse, type ReactActEnvironmentGlobal } from "./observability-panels.test-fixtures";
import { RoutingEventDetailsPanel } from "./routing-event-details-panel";
import { RouteRow } from "./routing-route-row";

function Harness({ event }: { event: AnalyticsRouteEvent }) {
  const [selected, setSelected] = useState<AnalyticsRouteEvent | null>(null);
  return (
    <>
      <table><tbody><RouteRow event={event} onViewDetails={setSelected} /></tbody></table>
      {selected ? <RoutingEventDetailsPanel event={selected} onClose={() => setSelected(null)} /> : null}
    </>
  );
}

function detailValue(label: string) {
  const term = Array.from(document.querySelectorAll('[role="dialog"] dt')).find((node) => node.textContent === label);
  return term?.nextElementSibling?.textContent;
}

function click(element: Element | null) {
  expect(element).not.toBeNull();
  act(() => element?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
}

describe("routing event details", () => {
  let container: HTMLDivElement;
  let root: Root;
  let previousOverflow: string;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    previousOverflow = document.body.style.overflow;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.style.overflow = previousOverflow;
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("keeps long guardrails out of the row and shows every warning and full field on click", () => {
    const event = {
      ...routeEventsResponse().events[0],
      agent_id: `agent-${"identifier-".repeat(12)}`,
      parent_session_id: "parent-session-full-id",
      child_session_id: "child-session-full-id",
      trace_id: "trace-full-id",
      batch_id: "batch-full-id",
      goal: `完整目标\n${"目标内容".repeat(60)}`,
      reason: "完整决策原因".repeat(40),
      fallback_reason: "完整回退原因".repeat(40),
      warnings: [
        `difficulty_promoted_by_keyword:${"very-long-keyword-".repeat(30)}`,
        "difficulty_floor_by_task_type:security",
        `unknown_guardrail:\n${"x".repeat(800)}`,
      ],
    };
    act(() => root.render(<Harness event={event} />));
    const row = container.querySelector("tbody tr");
    expect(row?.textContent).toContain("3 条");
    for (const value of [...event.warnings, event.goal, event.reason, event.fallback_reason, event.agent_id]) {
      expect(row?.textContent).not.toContain(value);
    }
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    click(row);
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.querySelectorAll("li")).toHaveLength(3);
    expect(dialog?.textContent).toContain("关键词提升：");
    expect(dialog?.textContent).toContain("任务类型下限：security");
    for (const warning of event.warnings) expect(dialog?.textContent).toContain(warning);
    expect(detailValue("Agent ID")).toBe(event.agent_id);
    expect(detailValue("任务主体")).toBe(event.task_subject);
    expect(detailValue("目标")).toBe(event.goal);
    expect(detailValue("原因")).toBe(event.reason);
    expect(detailValue("回退原因")).toBe(event.fallback_reason);
    expect(detailValue("父会话 ID")).toBe(event.parent_session_id);
    expect(detailValue("子会话 ID")).toBe(event.child_session_id);
    expect(detailValue("Trace ID")).toBe(event.trace_id);
    expect(detailValue("批次 ID")).toBe(event.batch_id);
    expect(detailValue("候选数量")).toBe("2");
    expect(detailValue("推理强度")).toBe("max");
    expect(detailValue("尝试")).toBe("1");
    expect(detailValue("最大尝试次数")).toBe("2");
  });

  it.each([
    { value: true, route: "改道", fallback: "回退" },
    { value: false, route: "未改道", fallback: "未回退" },
    { value: undefined, route: "未记录", fallback: "未记录" },
  ])("preserves tri-state flags ($value), missing fields and recorded zeroes", ({ value, route, fallback }) => {
    const event: AnalyticsRouteEvent = {
      recorded_at: "2026-09-27T00:00:00Z",
      session_id: "main-session",
      scope: "main_agent",
      kind: "applied",
      reason: "",
      step: 0,
      attempt: 0,
      candidate_count: 0,
      route_changed: value,
      fallback_used: value,
    };
    act(() => root.render(<Harness event={event} />));
    expect(container.querySelector("tbody tr")?.textContent).toContain(`${route} / ${fallback}`);
    click(container.querySelector("button"));
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("暂无护栏告警");
    expect(detailValue("改道")).toBe(route);
    expect(detailValue("回退")).toBe(fallback);
    expect(detailValue("步骤")).toBe("0");
    expect(detailValue("尝试")).toBe("0");
    expect(detailValue("候选数量")).toBe("0");
    expect(detailValue("最大尝试次数")).toBe("未记录");
    expect(detailValue("模型")).toBe("未记录");
    expect(detailValue("目标")).toBe("未记录");
    expect(detailValue("子会话 ID")).toBe("未记录");
    expect(detailValue("作用域")).toBe("主 Agent");
  });

  it("supports close, Escape, backdrop dismissal and restores focus and scrolling", () => {
    document.body.style.overflow = "auto";
    act(() => root.render(<Harness event={routeEventsResponse().events[0]} />));
    const trigger = container.querySelector("button");
    click(trigger);
    const close = document.querySelector<HTMLButtonElement>('[aria-label="关闭明细"]');
    expect(document.activeElement).toBe(close);
    expect(document.body.style.overflow).toBe("hidden");

    // Tab stays in the drawer, including its keyboard-scrollable content region.
    act(() => close?.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true })));
    const scrollArea = document.querySelector<HTMLElement>('[role="dialog"] [tabindex="0"]');
    expect(document.activeElement).toBe(scrollArea);
    act(() => scrollArea?.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })));
    expect(document.activeElement).toBe(close);

    click(close);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(document.body.style.overflow).toBe("auto");

    click(trigger);
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    click(trigger);
    const overlay = document.querySelector('[role="dialog"]')?.parentElement;
    act(() => overlay?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(document.body.style.overflow).toBe("auto");
  });
});
