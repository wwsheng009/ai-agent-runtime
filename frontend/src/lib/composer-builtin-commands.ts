// P2-7：内置命令清单与参数解析（命令行机制层见 `lib/composer-commands.ts`）。
//
// 边界（与计划 §6.3 P2-7 对齐）：
// - 只注册**当前确实可执行**的命令：`/export`（会话轨迹 JSONL 导出）、
//   `/rename`（会话重命名）、`/feedback`（log-only 反馈入口，无上报渠道）与
//   `/model`（模型选择：宿主目录驱动候选 + 无参数弹窗）。执行器接线见
//   `use-composer-command-executor.ts`；
// - `/model` 的候选项由宿主持有（真实运行时目录，见 `lib/composer-model-options.ts`），
//   清单本身不内置任何模型名——没有目录就没有候选，不伪造；
// - `/feedback` 是「log-only」入口：只写本地结构化日志，**不声称已上报**
//   （本仓无反馈后端路由与外部渠道，回执文案如实说明）；
// - 参数解析一律「解析不了就报错」：不猜用户意图、不把非法参数降级为默认行为。

import type {
  ComposerCommandDefinition,
  ComposerCommandOption,
} from "./composer-commands";

export type ComposerBuiltinCommandHostOptions = {
  /** `/model` 的第二级候选（宿主用真实运行时目录组装）；缺省 / 空数组 = 无候选。 */
  modelOptions?: readonly ComposerCommandOption[];
};

/** 组装内置命令清单；宿主数据（`/model` 候选）由调用方注入，清单本身不持有模型名。 */
export function buildComposerBuiltinCommands(
  hostOptions: ComposerBuiltinCommandHostOptions = {},
): readonly ComposerCommandDefinition[] {
  const modelOptions = hostOptions.modelOptions ?? [];
  return [
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
    {
      name: "feedback",
      kind: "execute",
      descriptionKey: "composer.builtin.feedback.description",
      argumentHintKey: "composer.builtin.feedback.argumentHint",
    },
    {
      name: "model",
      kind: "popupSelect",
      descriptionKey: "composer.builtin.model.description",
      argumentHintKey: "composer.builtin.model.argumentHint",
      options: modelOptions.length > 0 ? modelOptions : undefined,
    },
  ];
}

/** 无宿主数据的内置清单（无 `/model` 候选；命令仍可提交以打开弹窗）。 */
export const COMPOSER_BUILTIN_COMMANDS: readonly ComposerCommandDefinition[] =
  buildComposerBuiltinCommands();

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
 * 单段文本参数的公共归一化：去首尾空白；整段被单/双引号包住时脱壳
 * （方便以引号书写带首尾空白的文本）。归一化后为空即「无有效内容」，
 * 由各命令自行决定是报错还是走默认行为。
 */
function normalizeArgumentText(raw: string): string {
  const trimmed = raw.trim();
  const quoted =
    trimmed.length >= 2 &&
    ((trimmed.startsWith('"') && trimmed.endsWith('"')) ||
      (trimmed.startsWith("'") && trimmed.endsWith("'")));
  return quoted ? trimmed.slice(1, -1).trim() : trimmed;
}

/**
 * `/rename <title>` 参数解析：去掉首尾空白；整段被单/双引号包住时脱壳
 * （方便以引号书写带首尾空白的标题）。标题为空即失败，绝不生成空标题。
 */
export function parseRenameCommandArgs(raw: string): RenameArgsParseResult {
  const title = normalizeArgumentText(raw);
  if (title.length === 0) {
    return { ok: false, reason: "empty" };
  }
  return { ok: true, title };
}

export type FeedbackArgsParseResult =
  | { ok: true; message: string }
  | { ok: false; reason: "empty" };

/**
 * `/feedback <text>` 参数解析：与 `/rename` 同一归一化口径。
 * 空反馈失败（不产生空日志行）；本命令为 log-only，不做任何远端提交。
 */
export function parseFeedbackCommandArgs(raw: string): FeedbackArgsParseResult {
  const message = normalizeArgumentText(raw);
  if (message.length === 0) {
    return { ok: false, reason: "empty" };
  }
  return { ok: true, message };
}
