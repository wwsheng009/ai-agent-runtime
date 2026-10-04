// @vitest-environment jsdom

// `$` Tab 公共前缀延伸 + select 回声抑制 + `+` 菜单插入的 hook 级回归。
// 组件级技能用例在 message-composer-menu.test.tsx（该文件受 500 非空行门禁约束），
// 这里直接驱动 hook，能精确模拟「程序化写入后旧 token 回声」的时序。

import { act, useEffect, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { type ComposerMenuSnapshot, type ComposerReferenceGroup } from "@/lib/composer-menu";
import { useComposerMenu } from "./use-composer-menu";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SKILL_GROUPS: readonly ComposerReferenceGroup[] = [
  {
    id: "skills",
    label: "技能",
    items: [
      { id: "docx", label: "docx", insertText: "docx" },
      { id: "docs", label: "docs", insertText: "docs" },
    ],
    emptyText: "未找到匹配技能",
  },
];

const REFERENCE_GROUPS: readonly ComposerReferenceGroup[] = [
  {
    id: "artifacts",
    label: "文件",
    items: [{ id: "a1", label: "app.tsx", insertText: "src/app.tsx" }],
  },
];

type Controller = {
  value: string;
  open: boolean;
  snapshot: ComposerMenuSnapshot;
  handleValueChange: (next: string, caret: number) => void;
  handleCaretChange: (caret: number) => void;
  handleKeyDown: (event: KeyboardEvent) => boolean;
  selectItem: (itemId: string) => void;
  openFromButton: () => void;
};

function Harness({ controllerRef }: { controllerRef: { current: Controller | null } }) {
  const [value, setValue] = useState("");
  const menu = useComposerMenu({
    value,
    commands: [],
    referenceGroups: REFERENCE_GROUPS,
    skillGroups: SKILL_GROUPS,
    hasAttachAction: false,
    attachLabel: "attach",
    onValueChange: (next) => setValue(next),
    onAttachRequest: () => {},
  });
  // 测试台：每次渲染刷新控制器（effect 内写 ref，满足 react-hooks/immutability）。
  useEffect(() => {
    controllerRef.current = {
      value,
      open: menu.open,
      snapshot: menu.snapshot,
      handleValueChange: menu.handleValueChange,
      handleCaretChange: menu.handleCaretChange,
      handleKeyDown: (event) =>
        menu.handleKeyDown(event as unknown as ReactKeyboardEvent<HTMLTextAreaElement>),
      selectItem: menu.selectItem,
      openFromButton: menu.openFromButton,
    };
  });
  return null;
}

function tabEvent(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
}

describe("useComposerMenu skill tab extension", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controllerRef: { current: Controller | null };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controllerRef = { current: null };
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    act(() => root?.render(<Harness controllerRef={controllerRef} />));
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("extends the common prefix once, ignores the stale select echo, then accepts the pick", () => {
    act(() => controllerRef.current?.handleValueChange("$do", 3));
    expect(controllerRef.current?.open).toBe(true);

    // 第一次 Tab：只延伸公共前缀 "doc"，不落空格、菜单保持打开。
    let handled = false;
    act(() => {
      handled = controllerRef.current?.handleKeyDown(tabEvent()) ?? false;
    });
    expect(handled).toBe(true);
    expect(controllerRef.current?.value).toBe("$doc");
    expect(controllerRef.current?.open).toBe(true);

    // 程序化写入后的旧 token 回声（光标退回到前缀内）：必须被忽略，锚点保持 "$doc"，
    // 否则第二次 Tab 会在 `$doc` 上再插一次前缀（写坏成 "$docc"）。
    act(() => controllerRef.current?.handleCaretChange(3));
    expect(controllerRef.current?.value).toBe("$doc");
    expect(controllerRef.current?.open).toBe(true);

    // 第二次 Tab：前缀不可再加深 → 接受当前高亮项 docx，补全为 `$docx ` 并关闭菜单。
    let accepted = false;
    act(() => {
      accepted = controllerRef.current?.handleKeyDown(tabEvent()) ?? false;
    });
    expect(accepted).toBe(true);
    expect(controllerRef.current?.value).toBe("$docx ");
    expect(controllerRef.current?.open).toBe(false);
  });

  it("inserts a skill from the + menu at the caret (no glued text)", () => {
    act(() => controllerRef.current?.handleValueChange("hello world", 5));
    act(() => controllerRef.current?.openFromButton());
    expect(controllerRef.current?.open).toBe(true);
    // `+` 根层给技能 launcher（点选下钻后在光标处落 `$name `，不再是死入口）。
    expect(controllerRef.current?.snapshot.groups.map((group) => group.id)).toContain("skills");

    act(() => controllerRef.current?.selectItem("launcher:skills"));
    act(() => controllerRef.current?.selectItem("skill:skills:docx"));
    expect(controllerRef.current?.value).toBe("hello $docx world");
    expect(controllerRef.current?.open).toBe(false);
  });

  it("inserts a skill at the end of the draft with a trailing space", () => {
    act(() => controllerRef.current?.openFromButton());
    act(() => controllerRef.current?.selectItem("launcher:skills"));
    act(() => controllerRef.current?.selectItem("skill:skills:docs"));
    expect(controllerRef.current?.value).toBe("$docs ");
  });

  it("inserts a reference from the + menu at the caret", () => {
    act(() => controllerRef.current?.handleValueChange("see  for details", 4));
    act(() => controllerRef.current?.openFromButton());
    act(() => controllerRef.current?.selectItem("launcher:artifacts"));
    act(() => controllerRef.current?.selectItem("reference:artifacts:a1"));
    expect(controllerRef.current?.value).toBe("see @src/app.tsx for details");
    expect(controllerRef.current?.open).toBe(false);
  });
});
