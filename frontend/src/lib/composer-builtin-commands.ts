// P2-7：内置命令清单与参数解析（命令行机制层见 `lib/composer-commands.ts`）。
//
// 边界（与计划 §6.3 P2-7 对齐）：
// - 只注册**当前确实可执行**的命令：`/export`（会话轨迹 JSONL 导出）与
//   `/rename`（会话重命名）。执行器接线见 `use-composer-command-executor.ts`；
// - 「反馈入口」在本仓既无后端路由、也无外部渠道，按「不放置死按钮」**不注册**，
//   缺口留在计划台账（注册一个点了没反应的命令等于伪造能力）；
// - 参数解析一律「解析不了就报错」：不猜用户意图、不把非法参数降级为默认行为。

import type { ComposerCommandDefinition } from "./composer-commands";

export const COMPOSER_BUILTIN_COMMANDS: readonly ComposerCommandDefinition[] = [
  {
    name: "export",
    kind: "action",
    descriptionKey: "composer.builtin.export.description",
    argumentHintKey: "composer.builtin.export.argumentHint",
  },
  {
    name: "rename",
    kind: "execute",
    descriptionKey: "composer.builtin.rename.description",
    argumentHintKey: "composer.builtin.rename.argumentHint",
  },
];

export type ExportCommandArgs = {
  /** `--redact`：脱敏导出（与轨迹视图的导出脱敏开关同一实现）。 */
  redact: boolean;
};

export type ExportArgsParseError =
  | { kind: "unknown-flag"; flag: string }
  | { kind: "unexpected-argument"; value: string };

export type ExportArgsParseResult =
  | { ok: true; args: ExportCommandArgs }
  | { ok: false; error: ExportArgsParseError };

/**
 * `/export [--redact]` 参数解析。
 * 仅接受 `--redact`；未知开关与非开关位置参数都失败（附原文，供提示回显）。
 */
export function parseExportCommandArgs(raw: string): ExportArgsParseResult {
  const tokens = raw.split(/\s+/).filter((token) => token.length > 0);
  let redact = false;
  for (const token of tokens) {
    if (token === "--redact") {
      redact = true;
      continue;
    }
    if (token.startsWith("-")) {
      return { ok: false, error: { kind: "unknown-flag", flag: token } };
    }
    return { ok: false, error: { kind: "unexpected-argument", value: token } };
  }
  return { ok: true, args: { redact } };
}

export type RenameArgsParseResult =
  | { ok: true; title: string }
  | { ok: false; reason: "empty" };

/**
 * `/rename <title>` 参数解析：去掉首尾空白；整段被单/双引号包住时脱壳
 * （方便以引号书写带首尾空白的标题）。标题为空即失败，绝不生成空标题。
 */
export function parseRenameCommandArgs(raw: string): RenameArgsParseResult {
  const trimmed = raw.trim();
  if (trimmed.length === 0) {
    return { ok: false, reason: "empty" };
  }
  const quoted =
    trimmed.length >= 2 &&
    ((trimmed.startsWith('"') && trimmed.endsWith('"')) ||
      (trimmed.startsWith("'") && trimmed.endsWith("'")));
  const title = quoted ? trimmed.slice(1, -1).trim() : trimmed;
  if (title.length === 0) {
    return { ok: false, reason: "empty" };
  }
  return { ok: true, title };
}
