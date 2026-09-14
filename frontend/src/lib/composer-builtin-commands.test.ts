// P2-7：内置命令清单与参数解析单测（纯函数，无 DOM）。
//
// 覆盖口径：
// - 清单能进入注册表（名字合法、无重名冲突）；
// - 参数解析「解析不了就报错」：未知开关 / 多余位置参数 / 空标题一律失败，
//   不猜测、不降级（真实执行路径的失败透出见 use-composer-command-executor 测试）。

import { describe, expect, it } from "vitest";

import {
  COMPOSER_BUILTIN_COMMANDS,
  parseExportCommandArgs,
  parseRenameCommandArgs,
} from "./composer-builtin-commands";
import { createComposerCommandRegistry } from "./composer-commands";

describe("COMPOSER_BUILTIN_COMMANDS", () => {
  it("内置命令可进注册表且无重名（构造即校验）", () => {
    const registry = createComposerCommandRegistry(COMPOSER_BUILTIN_COMMANDS);
    expect(registry.commands.map((command) => command.key)).toEqual([
      "export",
      "rename",
    ]);
    expect(registry.byKey.get("export")?.kind).toBe("action");
    expect(registry.byKey.get("rename")?.kind).toBe("execute");
  });

  it("不注册无执行器的命令（如 /feedback 无后端路由与外部渠道）", () => {
    const registry = createComposerCommandRegistry(COMPOSER_BUILTIN_COMMANDS);
    expect(registry.byKey.has("feedback")).toBe(false);
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
