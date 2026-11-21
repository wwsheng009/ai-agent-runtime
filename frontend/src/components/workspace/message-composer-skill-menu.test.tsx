// @vitest-environment jsdom

import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  menu,
  press,
  renderComposer,
  SKILL_GROUPS,
  textarea,
  typeText,
} from "./message-composer-menu-test-support";
import {
  composerTestContainer,
  installComposerTestLifecycle,
} from "./message-composer-test-support";

describe("MessageComposer skill menu", () => {
  installComposerTestLifecycle();

  let container: HTMLDivElement;

  beforeEach(() => {
    container = composerTestContainer();
  });

  it("opens the skill list on $ and completes the picked skill with a trailing space", () => {
    const onDraftChange = vi.fn();
    renderComposer({ skillGroups: SKILL_GROUPS, onDraftChange });

    typeText("$");
    expect(menu()).not.toBeNull();
    const options = Array.from(
      container.querySelectorAll("[data-composer-skill-option]"),
    );
    expect(
      options.map((option) =>
        option.getAttribute("data-composer-skill-option"),
      ),
    ).toEqual(["brand", "docx"]);
    // 技能名必须完整展示：不得省略号截断（必要时折行），描述限宽为次级信息。
    const label = options[0].querySelector("span");
    expect(label?.className).toContain("break-all");
    expect(label?.className).not.toContain("truncate");
    const description = options[0].querySelectorAll("span")[1];
    expect(description?.className).toContain("max-w-[40%]");
    expect(description?.className).toContain("truncate");

    typeText("$br");
    expect(
      Array.from(
        container.querySelectorAll("[data-composer-skill-option]"),
      ).map((option) => option.getAttribute("data-composer-skill-option")),
    ).toEqual(["brand"]);

    press("Enter");
    expect(onDraftChange).toHaveBeenLastCalledWith("$brand ");
    expect(menu()).toBeNull();
  });

  it("supports multiple skill mentions in one draft", () => {
    const onDraftChange = vi.fn();
    renderComposer({ skillGroups: SKILL_GROUPS, onDraftChange });

    typeText("$br");
    press("Enter");
    expect(onDraftChange).toHaveBeenLastCalledWith("$brand ");

    // 受控 harness 已回灌 "$brand "；第二个提及沿用同一条补全路径。
    typeText("$brand 按照 $do");
    press("Enter");
    expect(onDraftChange).toHaveBeenLastCalledWith("$brand 按照 $docx ");
  });

  it("reports skills mode to the host so file fetches stay off", () => {
    const onMenuStateChange = vi.fn();
    renderComposer({ skillGroups: SKILL_GROUPS, onMenuStateChange });

    typeText("$do");
    expect(onMenuStateChange).toHaveBeenLastCalledWith({
      open: true,
      mode: "skills",
      query: "do",
    });
  });

  it("keeps unmatched skill tokens as plain text instead of blocking Enter", () => {
    renderComposer({ skillGroups: SKILL_GROUPS });
    typeText("$zzz");

    expect(container.querySelector("[data-composer-menu]")).not.toBeNull();
    const note = container.querySelector(
      "[data-composer-menu-group-note='skills']",
    );
    expect(note?.textContent).toBe("未找到匹配技能");

    const event = new KeyboardEvent("keydown", {
      key: "Enter",
      bubbles: true,
      cancelable: true,
    });
    act(() => {
      textarea().dispatchEvent(event);
    });
    // 未命中技能是合法普通文本（对齐 TUI「未命中不报错」）：Enter 不被拦截。
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not open the skill menu inside inline code", () => {
    renderComposer({ skillGroups: SKILL_GROUPS });
    typeText("bash `$br");
    expect(menu()).toBeNull();
  });
});