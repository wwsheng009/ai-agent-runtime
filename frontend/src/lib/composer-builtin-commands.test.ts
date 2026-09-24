// P2-7：内置命令清单与参数解析单测（纯函数，无 DOM）。
//
// 覆盖口径：
// - 清单能进入注册表（名字合法、无重名冲突）；
// - 参数解析「解析不了就报错」：未知开关 / 多余位置参数 / 空标题一律失败，
//   不猜测、不降级（真实执行路径的失败透出见 use-composer-command-executor 测试）。

import { describe, expect, it } from "vitest";

import {
  buildComposerBuiltinCommands,
  COMPOSER_BUILTIN_COMMANDS,
  parseExportCommandArgs,
  parseFeedbackCommandArgs,
  parseProfileSaveAsCommandArgs,
  parseRenameCommandArgs,
} from "./composer-builtin-commands";
import { createComposerCommandRegistry } from "./composer-commands";

describe("COMPOSER_BUILTIN_COMMANDS", () => {
  it("内置命令可进注册表且无重名（构造即校验）", () => {
    const registry = createComposerCommandRegistry(COMPOSER_BUILTIN_COMMANDS);
    expect(registry.commands.map((command) => command.key)).toEqual([
      "export",
      "rename",
      "feedback",
      "model",
      "skill",
    ]);
    expect(registry.byKey.get("export")?.kind).toBe("action");
    expect(registry.byKey.get("rename")?.kind).toBe("execute");
    // `/model` 的第二级候选由宿主目录注入；无目录时命令仍在位（提交即打开弹窗）。
    expect(registry.byKey.get("feedback")?.kind).toBe("execute");
    expect(registry.byKey.get("model")?.kind).toBe("popupSelect");
    expect(registry.byKey.get("model")?.options).toBeUndefined();
    // `/skill` 同 `/model`：候选由宿主 skill 目录注入；无目录时打开弹窗。
    expect(registry.byKey.get("skill")?.kind).toBe("popupSelect");
  });

  it("宿主注入目录时 /model 携带候选；空目录不产生占位候选", () => {
    const options = [
      { value: "deepseek-chat", label: "deepseek-chat", description: "deepseek" },
    ];
    const withCatalog = createComposerCommandRegistry(
      buildComposerBuiltinCommands({ modelOptions: options }),
    );
    expect(withCatalog.byKey.get("model")?.options).toEqual(options);

    const emptyCatalog = createComposerCommandRegistry(
      buildComposerBuiltinCommands({ modelOptions: [] }),
    );
    expect(emptyCatalog.byKey.get("model")?.options).toBeUndefined();
  });
});

describe("parseExportCommandArgs", () => {
  it("默认不脱敏；`--redact` 打开脱敏；重复开关不报错", () => {
    expect(parseExportCommandArgs("")).toEqual({ ok: true, args: { redact: false } });
    expect(parseExportCommandArgs("   ")).toEqual({
      ok: true,
      args: { redact: false },
    });
    expect(parseExportCommandArgs("--redact")).toEqual({
      ok: true,
      args: { redact: true },
    });
    expect(parseExportCommandArgs("--redact --redact")).toEqual({
      ok: true,
      args: { redact: true },
    });
  });

  it("未知开关带原文失败，不回落默认行为", () => {
    expect(parseExportCommandArgs("--json")).toEqual({
      ok: false,
      error: { kind: "unknown-flag", flag: "--json" },
    });
    expect(parseExportCommandArgs("--redact --json")).toEqual({
      ok: false,
      error: { kind: "unknown-flag", flag: "--json" },
    });
  });

  it("非开关位置参数失败（不静默忽略）", () => {
    expect(parseExportCommandArgs("now")).toEqual({
      ok: false,
      error: { kind: "unexpected-argument", value: "now" },
    });
  });
});

describe("parseRenameCommandArgs", () => {
  it("去首尾空白后作为标题", () => {
    expect(parseRenameCommandArgs("  New title  ")).toEqual({
      ok: true,
      title: "New title",
    });
  });

  it("整段引号包住时脱壳（可书写带首尾空白的标题）", () => {
    expect(parseRenameCommandArgs('" padded title "')).toEqual({
      ok: true,
      title: "padded title",
    });
    expect(parseRenameCommandArgs("'single quoted'")).toEqual({
      ok: true,
      title: "single quoted",
    });
  });

  it("空标题失败：不生成空标题", () => {
    expect(parseRenameCommandArgs("")).toEqual({ ok: false, reason: "empty" });
    expect(parseRenameCommandArgs("   ")).toEqual({ ok: false, reason: "empty" });
    expect(parseRenameCommandArgs('""')).toEqual({ ok: false, reason: "empty" });
    expect(parseRenameCommandArgs('"  "')).toEqual({ ok: false, reason: "empty" });
  });
});

describe("parseFeedbackCommandArgs", () => {
  it("去首尾空白后作为反馈正文（与 /rename 同一归一化口径）", () => {
    expect(parseFeedbackCommandArgs("  the menu is great  ")).toEqual({
      ok: true,
      message: "the menu is great",
    });
    expect(parseFeedbackCommandArgs('" kept inner spaces "')).toEqual({
      ok: true,
      message: "kept inner spaces",
    });
  });

  it("空反馈失败：不产生空日志行", () => {
    expect(parseFeedbackCommandArgs("")).toEqual({ ok: false, reason: "empty" });
    expect(parseFeedbackCommandArgs("   ")).toEqual({
      ok: false,
      reason: "empty",
    });
    expect(parseFeedbackCommandArgs('""')).toEqual({
      ok: false,
      reason: "empty",
    });
  });
});

describe("parseProfileSaveAsCommandArgs", () => {
  it("名称 + 可选 --to：缺省层为空串（由后端按默认层落盘）", () => {
    expect(parseProfileSaveAsCommandArgs(" my-review ")).toEqual({
      ok: true,
      args: { name: "my-review", layer: "" },
    });
    expect(parseProfileSaveAsCommandArgs("my-review --to project")).toEqual({
      ok: true,
      args: { name: "my-review", layer: "project" },
    });
    // 开关在前也认（不依赖书写顺序），但位置参数仍只允许一个。
    expect(parseProfileSaveAsCommandArgs("--to user my-review")).toEqual({
      ok: true,
      args: { name: "my-review", layer: "user" },
    });
  });

  it("缺名失败：不生成无名 profile", () => {
    expect(parseProfileSaveAsCommandArgs("")).toEqual({
      ok: false,
      error: { kind: "need-name" },
    });
    expect(parseProfileSaveAsCommandArgs("   --to project")).toEqual({
      ok: false,
      error: { kind: "need-name" },
    });
  });

  it("非法层如实报错（不静默落到默认层）", () => {
    expect(parseProfileSaveAsCommandArgs("my-review --to team")).toEqual({
      ok: false,
      error: { kind: "bad-layer", value: "team" },
    });
    // `--to` 后没有值：同样是层非法，而不是「按缺省处理」。
    expect(parseProfileSaveAsCommandArgs("my-review --to")).toEqual({
      ok: false,
      error: { kind: "bad-layer", value: "" },
    });
  });

  it("未知开关与多余位置参数一律失败（附原文）", () => {
    expect(parseProfileSaveAsCommandArgs("my-review --force")).toEqual({
      ok: false,
      error: { kind: "unknown-flag", flag: "--force" },
    });
    expect(parseProfileSaveAsCommandArgs("my-review other")).toEqual({
      ok: false,
      error: { kind: "unexpected-argument", value: "other" },
    });
  });
});
