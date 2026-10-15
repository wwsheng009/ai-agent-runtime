// S2b（docs/bots/13 §3.1/§3.4）：订阅编辑器纯逻辑门禁——面板分组、形态推导、
// 相容性扫描与一键拆分；口径与微前端 web/js/connectors.js 一致。

import { describe, expect, it } from "vitest";

import {
  DELIVER_ALL,
  EVENT_TYPE_FORMS,
  EVENT_TYPE_ORDER,
  customPanelTypes,
  eventTypeSections,
  mergeCustomTokens,
  resolveSubscriptionDeliver,
  scanSubscriptionTypes,
  splitSubscriptionModel,
  toggleType,
  typesSummary,
  visibleEventTypeSections,
  withResolvedDeliver,
} from "./subscription-forms";

describe("eventTypeSections", () => {
  it("分组覆盖全部条目：组键 = 允许形态 join('/')，顺序与微前端一致", () => {
    const sections = eventTypeSections();
    expect(sections.map((section) => section.form)).toEqual([
      "content",
      "stream",
      "notice",
      "notice/digest",
      "digest",
      "",
    ]);
    expect(sections.flatMap((section) => section.types)).toEqual([...EVENT_TYPE_ORDER]);
  });

  it("同组条目携带相同形态集合（types↔deliver 关联由结构表达）", () => {
    for (const section of eventTypeSections()) {
      for (const name of section.types) {
        expect(EVENT_TYPE_FORMS[name].join("/")).toBe(section.form);
      }
    }
  });

  it("未接线组收纳 forms 为空的词表条目", () => {
    const unwired = eventTypeSections().find((section) => section.form === "");
    expect(unwired?.types).toContain("user_message");
    expect(unwired?.types).toContain("error");
    expect(unwired?.types).not.toContain("assistant_message");
  });
});

describe("visibleEventTypeSections", () => {
  it("按当前投递形态过滤：仅保留允许形态包含该形态的组", () => {
    expect(visibleEventTypeSections("notice", []).map((section) => section.form)).toEqual([
      "notice",
      "notice/digest",
    ]);
    expect(visibleEventTypeSections("content", []).map((section) => section.form)).toEqual(["content"]);
    expect(visibleEventTypeSections("digest", []).map((section) => section.form)).toEqual([
      "notice/digest",
      "digest",
    ]);
    expect(visibleEventTypeSections("stream", []).map((section) => section.form)).toEqual(["stream"]);
  });

  it("已勾选类型所在组保留可见（跨形态存量值可就地取消）", () => {
    expect(visibleEventTypeSections("notice", ["assistant_message"]).map((section) => section.form)).toEqual([
      "content",
      "notice",
      "notice/digest",
    ]);
    expect(visibleEventTypeSections("notice", ["my.custom"]).map((section) => section.form)).toEqual([
      "notice",
      "notice/digest",
    ]);
  });

  it("未接线组仅在其含勾选项时显示", () => {
    expect(visibleEventTypeSections("notice", []).map((section) => section.form)).not.toContain("");
    expect(visibleEventTypeSections("notice", ["user_message"]).map((section) => section.form)).toEqual([
      "notice",
      "notice/digest",
      "",
    ]);
  });
});

describe("scanSubscriptionTypes", () => {
  it("唯一形态类型给出冲突与允许集合（assistant_message 需要 content）", () => {
    const scan = scanSubscriptionTypes(["assistant_message", "turn.*"], "notice");
    expect(scan.allowed).toEqual(["content", "notice", "digest"]);
    expect(scan.conflicts).toEqual([{ type: "assistant_message", forms: ["content"] }]);
    expect(scan.unwired).toEqual([]);
  });

  it("未接线类型单独归入 unwired（勾选不会投递）", () => {
    const scan = scanSubscriptionTypes(["user_message", "session_start"], "notice");
    expect(scan.allowed).toEqual([]);
    expect(scan.conflicts).toEqual([]);
    expect(scan.unwired).toEqual(["user_message", "session_start"]);
  });

  it("词表外/空值不做静态判定；重复项去重", () => {
    const scan = scanSubscriptionTypes(["custom.thing", "turn.*", "turn.*", ""], "notice");
    expect(scan.allowed).toEqual(["notice", "digest"]);
    expect(scan.conflicts).toEqual([]);
    expect(scan.unwired).toEqual([]);
  });
});

describe("resolveSubscriptionDeliver", () => {
  it("唯一形态锁定", () => {
    const resolved = resolveSubscriptionDeliver(["assistant_message"], undefined);
    expect(resolved).toEqual({
      deliver: "content",
      options: ["content"],
      locked: true,
      free: false,
    });
  });

  it("当前形态不被允许时收敛到允许集合之首", () => {
    expect(resolveSubscriptionDeliver(["assistant_message"], "notice").deliver).toBe("content");
    expect(resolveSubscriptionDeliver(["completion"], "stream").deliver).toBe("notice");
  });

  it("多形态类型保持当前选择、选项收敛", () => {
    const resolved = resolveSubscriptionDeliver(["completion"], "digest");
    expect(resolved.deliver).toBe("digest");
    expect(resolved.options).toEqual(["notice", "digest"]);
    expect(resolved.locked).toBe(false);
    expect(resolved.free).toBe(false);
  });

  it("无可判定类型时自由选择（保留全量选项）", () => {
    const resolved = resolveSubscriptionDeliver(["custom.thing"], undefined);
    expect(resolved.free).toBe(true);
    expect(resolved.options).toEqual([...DELIVER_ALL]);
    expect(resolved.deliver).toBe("notice");
  });
});

describe("withResolvedDeliver", () => {
  it("形态合法时保持引用（无变化不回写）", () => {
    const sub = { id: "ops", types: ["turn.*"], deliver: "notice" as const };
    expect(withResolvedDeliver(sub)).toBe(sub);
  });

  it("形态不被允许时写回推导结果", () => {
    const next = withResolvedDeliver({ id: "ops", types: ["assistant_message"], deliver: "notice" });
    expect(next).toEqual({ id: "ops", types: ["assistant_message"], deliver: "content" });
  });
});

describe("toggleType", () => {
  it("勾选追加、取消移除、幂等不重复", () => {
    expect(toggleType(["turn.*"], "assistant_message", true)).toEqual([
      "turn.*",
      "assistant_message",
    ]);
    expect(toggleType(["turn.*", "assistant_message"], "turn.*", false)).toEqual([
      "assistant_message",
    ]);
    expect(toggleType(["turn.*"], "turn.*", true)).toEqual(["turn.*"]);
  });
});

describe("mergeCustomTokens", () => {
  it("新增 token 追加、既有词表外值不受影响", () => {
    expect(mergeCustomTokens(["turn.*", "my.custom"], [], ["approval"])).toEqual([
      "turn.*",
      "my.custom",
      "approval",
    ]);
  });

  it("输入框删除的 token 从 types 移除", () => {
    expect(mergeCustomTokens(["turn.*", "my.custom", "approval"], ["approval"], [])).toEqual([
      "turn.*",
      "my.custom",
    ]);
  });

  it("差分：删除消失项、保留交集、追加新增项", () => {
    expect(mergeCustomTokens(["a", "b"], ["a", "b"], ["b", "c"])).toEqual(["b", "c"]);
  });
});

describe("typesSummary", () => {
  it("去重计数，超过 3 项给出 more 标记", () => {
    expect(typesSummary(["a", "b", "a", "c", "d"])).toEqual({
      total: 4,
      shown: ["a", "b", "c"],
      more: true,
    });
    expect(typesSummary([])).toEqual({ total: 0, shown: [], more: false });
  });
});

describe("customPanelTypes", () => {
  it("词表外面板值保留原序（去重、去空白），面板内条目不再列出", () => {
    expect(customPanelTypes(["turn.*", "my.custom", "approval", "my.custom"])).toEqual([
      "my.custom",
      "approval",
    ]);
  });
});

describe("splitSubscriptionModel", () => {
  it("跨形态类型按形态拆行，id 追加 -2/-3；首行留在原索引", () => {
    const parts = splitSubscriptionModel({
      id: "ops",
      types: ["assistant_message", "turn.*"],
      deliver: "notice",
    });
    expect(parts).toEqual([
      { id: "ops", types: ["assistant_message"], deliver: "content" },
      { id: "ops-2", types: ["turn.*"], deliver: "notice" },
    ]);
  });

  it("词表外/未接线类型留在首条", () => {
    const parts = splitSubscriptionModel({
      id: "ops",
      types: ["user_message", "my.custom", "assistant_message", "turn.*"],
      deliver: "notice",
    });
    expect(parts).toEqual([
      { id: "ops", types: ["user_message", "my.custom", "assistant_message"], deliver: "content" },
      { id: "ops-2", types: ["turn.*"], deliver: "notice" },
    ]);
  });

  it("单一形态组不拆分（返回原对象）", () => {
    const sub = { id: "ops", types: ["agent.turn.finished", "turn.*"], deliver: "notice" as const };
    expect(splitSubscriptionModel(sub)).toEqual([sub]);
    expect(splitSubscriptionModel(sub)[0]).toBe(sub);
  });

  it("无 id 时不追加后缀", () => {
    const parts = splitSubscriptionModel({ types: ["assistant_message", "turn.*"], deliver: "notice" });
    expect(parts).toHaveLength(2);
    expect(parts[1].id).toBeUndefined();
  });
});