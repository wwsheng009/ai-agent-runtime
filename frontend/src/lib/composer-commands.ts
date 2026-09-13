// P1-4 子片 3：斜杠命令注册表与命令行判定（机制层；具体内置命令清单与执行器属 P2-7）。
// 契约（方案 §2 P1-4 第 2 条）：
// - 命令行显式四分类：leadingInput / popupSelect / action / execute；
// - 命令行永不静默降级为普通 prompt：未知/不完整命令行必须阻塞提交并给出可见原因；
// - 同名命令冲突 fail loudly（构造注册表即抛错，不做静默覆盖）。

export const COMPOSER_COMMAND_KINDS = [
  "leadingInput",
  "popupSelect",
  "action",
  "execute",
] as const;

export type ComposerCommandKind = (typeof COMPOSER_COMMAND_KINDS)[number];

export const DEFAULT_COMPOSER_COMMAND_KIND: ComposerCommandKind = "execute";

export type ComposerCommandDefinition = {
  /** 不含前导 `/`；大小写不敏感（查找键小写），展示沿用定义时的大小写。 */
  name: string;
  kind?: ComposerCommandKind;
  /** i18n key；模型层只持键，渲染层用 t() 解析。 */
  descriptionKey?: string;
  /** i18n key；参数提示（可选）。 */
  argumentHintKey?: string;
};

export type ComposerCommand = ComposerCommandDefinition & {
  kind: ComposerCommandKind;
  /** 归一化查找键：去前导斜杠、trim、小写。 */
  key: string;
};

export type ComposerCommandRegistry = {
  commands: readonly ComposerCommand[];
  byKey: ReadonlyMap<string, ComposerCommand>;
};

export class ComposerCommandNameError extends Error {
  readonly rawName: string;

  constructor(rawName: string) {
    super(`composer command name invalid: "${rawName}"`);
    this.name = "ComposerCommandNameError";
    this.rawName = rawName;
  }
}

export class ComposerCommandConflictError extends Error {
  readonly commandKey: string;

  constructor(commandKey: string) {
    super(`composer command already registered: "${commandKey}"`);
    this.name = "ComposerCommandConflictError";
    this.commandKey = commandKey;
  }
}

const COMPOSER_COMMAND_NAME_PATTERN = /^[a-z0-9][a-z0-9_-]*$/;

export function normalizeComposerCommandName(name: string): string {
  return name.trim().replace(/^\/+/, "").toLowerCase();
}

export function isValidComposerCommandName(name: string): boolean {
  return COMPOSER_COMMAND_NAME_PATTERN.test(name);
}

/**
 * 构造命令注册表。任何非法名或同名冲突都在此处 fail loudly，
 * 避免运行期出现「同名命令谁生效」的静默歧义。
 */
export function createComposerCommandRegistry(
  definitions: readonly ComposerCommandDefinition[],
): ComposerCommandRegistry {
  const commands: ComposerCommand[] = [];
  const byKey = new Map<string, ComposerCommand>();
  for (const definition of definitions) {
    const key = normalizeComposerCommandName(definition.name);
    if (!isValidComposerCommandName(key)) {
      throw new ComposerCommandNameError(definition.name);
    }
    if (byKey.has(key)) {
      throw new ComposerCommandConflictError(key);
    }
    const command: ComposerCommand = {
      ...definition,
      name: definition.name.trim().replace(/^\/+/, ""),
      kind: definition.kind ?? DEFAULT_COMPOSER_COMMAND_KIND,
      key,
    };
    commands.push(command);
    byKey.set(key, command);
  }
  return { commands: Object.freeze(commands), byKey };
}

export const EMPTY_COMPOSER_COMMAND_REGISTRY: ComposerCommandRegistry =
  createComposerCommandRegistry([]);

export function findComposerCommand(
  registry: ComposerCommandRegistry,
  name: string,
): ComposerCommand | undefined {
  return registry.byKey.get(normalizeComposerCommandName(name));
}

export type ComposerCommandLine = {
  /** 命令名（不含 `/`），可能为空串（只输入了 `/`）。 */
  name: string;
  /** 命令名之后的原始参数（已去掉紧随其后的空白）。 */
  args: string;
};

/**
 * 解析命令行。判定只取决于「行首（允许前导空白）是否为 `/`」：
 * 只要成立就是命令行，绝不因为命令名不合法而退回普通消息。
 */
export function parseComposerCommandLine(value: string): ComposerCommandLine | null {
  let start = 0;
  while (start < value.length && (value[start] === " " || value[start] === "\t")) {
    start += 1;
  }
  if (value[start] !== "/") {
    return null;
  }
  const rest = value.slice(start + 1);
  const separator = rest.search(/\s/);
  if (separator === -1) {
    return { name: rest, args: "" };
  }
  return {
    name: rest.slice(0, separator),
    args: rest.slice(separator).replace(/^\s+/, ""),
  };
}

export type ComposerSubmitClassification =
  | { kind: "empty" }
  | { kind: "prompt"; value: string }
  | { kind: "command"; command: ComposerCommand; args: string }
  | { kind: "incomplete-command" }
  | { kind: "unknown-command"; name: string };

/** 提交前判定：命令行永不降级为 prompt。 */
export function classifyComposerSubmit(
  value: string,
  registry: ComposerCommandRegistry,
): ComposerSubmitClassification {
  const parsed = parseComposerCommandLine(value);
  if (!parsed) {
    return value.trim().length === 0 ? { kind: "empty" } : { kind: "prompt", value };
  }
  if (parsed.name.length === 0) {
    return { kind: "incomplete-command" };
  }
  const command = findComposerCommand(registry, parsed.name);
  if (!command) {
    return { kind: "unknown-command", name: parsed.name };
  }
  return { kind: "command", command, args: parsed.args };
}
