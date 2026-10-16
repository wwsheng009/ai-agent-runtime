// Composer profile 切换器的会话绑定读取（Batch 12/13 的回显半程）。
//
// 后端把会话 profile 身份写在 `metadata.context`（`internal/sessionmeta`）：
//   - `profile_ref`：切换时解析出的引用（与运行时目录条目的 `ref` 同词汇）；
//   - `profile_name`：profile.yaml 声明的显示名；
//   - 旧数据可能是 legacy 键（`profile_reference` / `aicli_profile_name`），
//     读取按 canonical → legacy 的顺序回退。
//
// 空语义：会话未绑定 profile（新会话 / 未首轮 / 切换失败）→ 空字符串，
// UI 显示「未绑定/跟随默认」，绝不伪造一个 profile 名。

/** 与后端 sessionmeta.ProfileRef 一致。 */
export const SESSION_PROFILE_REF_CONTEXT_KEY = "profile_ref";
/** 与后端 sessionmeta.ProfileName 一致。 */
export const SESSION_PROFILE_NAME_CONTEXT_KEY = "profile_name";
/** sessionmeta.ProfileRef 的 legacy 别名（Web API 早期写入）。 */
export const SESSION_PROFILE_REF_LEGACY_CONTEXT_KEY = "profile_reference";
/** sessionmeta.ProfileName 的 legacy 别名（aicli 早期写入）。 */
export const SESSION_PROFILE_NAME_LEGACY_CONTEXT_KEY = "aicli_profile_name";

export type SessionProfileIdentity = {
  /** 绑定引用（可与目录候选 ref 匹配）；未绑定为空串。 */
  ref: string;
  /** 绑定 profile 的显示名；缺失时回落 ref。 */
  name: string;
};

function readContextString(context: unknown, keys: string[]): string {
  if (!context || typeof context !== "object") {
    return "";
  }
  const record = context as Record<string, unknown>;
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (trimmed.length > 0) {
        return trimmed;
      }
    }
  }
  return "";
}

/**
 * 从会话 context 读取 profile 身份；未绑定时 `{ ref: "", name: "" }`。
 * name 缺失但 ref 存在时回落 ref（保证触发器永远有可读文案）。
 */
export function readSessionProfileIdentity(
  context: unknown,
): SessionProfileIdentity {
  const ref = readContextString(context, [
    SESSION_PROFILE_REF_CONTEXT_KEY,
    SESSION_PROFILE_REF_LEGACY_CONTEXT_KEY,
  ]);
  const name = readContextString(context, [
    SESSION_PROFILE_NAME_CONTEXT_KEY,
    SESSION_PROFILE_NAME_LEGACY_CONTEXT_KEY,
  ]);
  return { ref, name: name || ref };
}
