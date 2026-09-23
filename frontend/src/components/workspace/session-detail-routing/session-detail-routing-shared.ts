// 「路由」区块的纯派生逻辑：草稿 → 补丁、错误 → 字段定位、层可写性判定。
// 与组件分离的原因同 session-detail-panel-shared：无 IO、无 React，可单测。
//
// 语义纪律（I-6）：本模块**只做集合运算与字符串匹配**，不推导 provider/model/
// effort，也不改写 source；唯一「判断」是「该行生效值是否来自本层覆盖」——
// 该判断直接读投影里的 `source` 字段，不自行比对配置文件。

import type {
  RoutingLevelSummary,
  RoutingPanelMetadata,
  SessionRoutingEditableField,
  SessionRoutingMainAgentPatch,
  SessionRoutingTargetLayer,
} from "@/types/runtime";
import { SESSION_ROUTING_EDITABLE_FIELDS } from "@/types/runtime";

/**
 * 面板内可编辑的三列草稿值（字符串；空串表示用户清空了输入框）。
 *
 * 键名与 `SESSION_ROUTING_EDITABLE_FIELDS` 一致（effort 列即 profile 的
 * `reasoning_effort`），这样「表格列 → 草稿 → 补丁」用的是同一套字段名。
 */
export type RoutingLevelDraft = {
  provider: string;
  model: string;
  reasoning_effort: string;
};

export type RoutingLevelDraftMap = Record<string, RoutingLevelDraft>;

/** 投影行 → 草稿初值：直接取后端给出的生效值，不做任何加工。 */
export function buildRoutingLevelDrafts(
  levels: readonly RoutingLevelSummary[],
): RoutingLevelDraftMap {
  const drafts: RoutingLevelDraftMap = {};
  for (const level of levels) {
    drafts[level.level] = {
      provider: level.provider,
      model: level.model,
      reasoning_effort: level.reasoning,
    };
  }
  return drafts;
}

export type SessionRoutingWritePlan = {
  mainAgent: SessionRoutingMainAgentPatch;
  clearFields: string[];
  /** false = 草稿与投影一致，无需发请求（避免无意义的写入与 actor 失效）。 */
  hasChanges: boolean;
};

/**
 * 草稿 → PATCH 补丁（§3.2 字段级合并 + §3.5.1 键路径清除）。
 *
 * 逐字段规则：
 *   * 草稿 == 投影值 → 跳过；
 *   * 草稿为空且该行 `source === layer` → 生成 `clear_fields`（清除本层覆盖，
 *     回到继承语义）；
 *   * 草稿为空但生效值来自**其它层** → 跳过：在本层清空不会改变任何东西
 *     （该字段在本层没有覆盖），UI 另有提示，不伪造「已清除」。
 */
export function buildSessionRoutingWrite(input: {
  levels: readonly RoutingLevelSummary[];
  drafts: RoutingLevelDraftMap;
  enabledDraft: boolean | null;
  effectiveEnabled: boolean;
  layer: SessionRoutingTargetLayer;
}): SessionRoutingWritePlan {
  const mainAgent: SessionRoutingMainAgentPatch = {};
  const clearFields: string[] = [];
  const profiles: Record<
    string,
    { provider?: string; model?: string; reasoning_effort?: string }
  > = {};

  if (input.enabledDraft !== null && input.enabledDraft !== input.effectiveEnabled) {
    mainAgent.enabled = input.enabledDraft;
  }

  for (const level of input.levels) {
    const draft = input.drafts[level.level];
    if (!draft) {
      continue;
    }
    const rowOverriddenAtLayer = level.source === input.layer;
    for (const field of SESSION_ROUTING_EDITABLE_FIELDS) {
      const baseline = readLevelField(level, field);
      const next = draft[field].trim();
      if (next === baseline) {
        continue;
      }
      if (next === "") {
        if (rowOverriddenAtLayer && baseline !== "") {
          clearFields.push(`main_agent.profiles.${level.level}.${field}`);
        }
        continue;
      }
      profiles[level.level] = {
        ...(profiles[level.level] ?? {}),
        [field]: next,
      };
    }
  }

  if (Object.keys(profiles).length > 0) {
    mainAgent.profiles = profiles;
  }

  return {
    mainAgent,
    clearFields,
    hasChanges:
      mainAgent.enabled !== undefined ||
      Object.keys(profiles).length > 0 ||
      clearFields.length > 0,
  };
}

function readLevelField(
  level: RoutingLevelSummary,
  field: SessionRoutingEditableField,
): string {
  if (field === "provider") {
    return level.provider;
  }
  if (field === "model") {
    return level.model;
  }
  return level.reasoning;
}

/** 后端投影里的 source 枚举（§6.1）。 */
export type RoutingSourceKey =
  | "session"
  | "workspace"
  | "config"
  | "default"
  | "derived";

/** 后端投影里的 source → i18n 子键；未知值返回空串（调用方原样展示）。 */
export function resolveRoutingSourceKey(source: string): RoutingSourceKey | "" {
  const normalized = source.trim().toLowerCase();
  switch (normalized) {
    case "session":
    case "workspace":
    case "config":
    case "default":
    case "derived":
      return normalized;
    default:
      return "";
  }
}

/** 层是否可写：只认后端 `writable_layers` 白名单；缺失即置灰（不自造入口）。 */
export function isRoutingLayerWritable(
  panel: RoutingPanelMetadata | null,
  layer: SessionRoutingTargetLayer,
): boolean {
  return panel?.writableLayers.includes(layer) ?? false;
}

/**
 * 写入目标回显：优先展示本次 PATCH 返回的 `target_path`（服务端事实），
 * 否则回落到 panel 里后端给出的层路径（workspace→chat-prefs.yaml，config→全局配置）。
 * session 层没有文件路径（覆盖随会话记录持久化），返回空串由 UI 用文案说明。
 */
export function resolveRoutingTargetPath(input: {
  panel: RoutingPanelMetadata | null;
  layer: SessionRoutingTargetLayer;
  serverTargetLayer: string;
  serverTargetPath: string;
}): string {
  const { layer, panel } = input;
  if (
    input.serverTargetPath &&
    input.serverTargetLayer.trim().toLowerCase() === layer
  ) {
    return input.serverTargetPath;
  }
  if (!panel) {
    return "";
  }
  if (layer === "workspace") {
    return panel.workspacePrefsPath;
  }
  if (layer === "config") {
    return panel.configPath;
  }
  return "";
}

export type RoutingErrorEcho = {
  /** 命中的档位；空串=无法定位到具体行（只做表单级展示）。 */
  level: string;
  /** 命中的字段；空串=只能定位到档位。 */
  field: SessionRoutingEditableField | "";
};

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** 档位/字段名按「非标识符字符」边界匹配，避免 hard 命中 extra_hard。 */
function containsToken(haystack: string, token: string): boolean {
  if (!token) {
    return false;
  }
  const pattern = new RegExp(
    `(^|[^A-Za-z0-9_])${escapeRegExp(token)}([^A-Za-z0-9_]|$)`,
  );
  return pattern.test(haystack);
}

/**
 * 把后端校验错误定位到具体档位/字段。
 *
 * 后端错误体是扁平字符串（`writeError` 只给 `error` 文本），没有结构化字段表，
 * 因此这里**只做文本定位**：命中 `profiles.<level>.<field>` 之类的键路径或档位
 * 标识符时，把该消息同时挂到对应行上；定位不到就只在表单级展示。
 * 消息文本本身原样透传，不改写、不翻译。
 */
export function resolveRoutingErrorEcho(
  message: string,
  levels: readonly string[],
): RoutingErrorEcho {
  const text = message.trim();
  if (!text) {
    return { level: "", field: "" };
  }

  for (const level of levels) {
    if (!containsToken(text, level)) {
      continue;
    }
    const field = SESSION_ROUTING_EDITABLE_FIELDS.find((candidate) =>
      text.includes(`${level}.${candidate}`),
    );
    return {
      level,
      field: field ?? SESSION_ROUTING_EDITABLE_FIELDS.find((candidate) =>
        containsToken(text, candidate),
      ) ?? "",
    };
  }

  return { level: "", field: "" };
}
