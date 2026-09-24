// 9 卡片编辑器的共享契约：卡片上下文 + 卡片 id 枚举 + issue 归属。
//
// 拆成独立 .ts 的理由：卡片是「无状态视图」，只消费这里定义的结构；
// 编辑器负责全部状态机（草稿 / 校验 / 保存 / 冲突），因此类型必须独立可测。

import type {
  RuntimeProfilePreviewReport,
  RuntimeProfileValidationReport,
  RuntimeProfileView,
} from "@/types/runtime";

import type { ProfileDraft, ProfileDraftIssue, ProfileDraftOverride } from "./profile-draft";

/** 9 张卡片的稳定顺序（导航与「上一张/下一张」共用）。 */
export type ProfileCardId =
  | "basic"
  | "tools"
  | "skills"
  | "mcp"
  | "prompts"
  | "agents"
  | "overrides"
  | "preferences"
  | "validation";

export const profileCardIds: ProfileCardId[] = [
  "basic",
  "tools",
  "skills",
  "mcp",
  "prompts",
  "agents",
  "overrides",
  "preferences",
  "validation",
];

/**
 * 卡片 → issue 路径前缀。
 * validation 卡是「汇总卡」，不带前缀（展示全部 issue），其余卡只认领自己那一段，
 * 这样导航角标能直接把用户带到出错的那张卡。
 */
const PROFILE_CARD_ISSUE_PREFIXES: Record<ProfileCardId, string[]> = {
  basic: ["name", "description"],
  tools: ["tools."],
  skills: ["skills."],
  mcp: ["mcp."],
  prompts: ["prompts."],
  agents: ["agents."],
  overrides: ["overrides"],
  preferences: ["preferences."],
  validation: [],
};

export function profileCardIssueCount(card: ProfileCardId, issues: ProfileDraftIssue[]) {
  const prefixes = PROFILE_CARD_ISSUE_PREFIXES[card];
  if (prefixes.length === 0) {
    return issues.length;
  }
  return issues.filter((issue) =>
    prefixes.some((prefix) => issue.path === prefix || issue.path.startsWith(prefix)),
  ).length;
}

/** issue 路径 → 归属卡片；本地校验失败时把用户直接送到出错的那张卡。 */
export function profileCardForIssuePath(path: string): ProfileCardId {
  for (const card of profileCardIds) {
    const prefixes = PROFILE_CARD_ISSUE_PREFIXES[card];
    if (
      prefixes.length > 0 &&
      prefixes.some((prefix) => path === prefix || path.startsWith(prefix))
    ) {
      return card;
    }
  }
  return "validation";
}

export type ProfileCardContext = {
  /** 相对已落盘 view 的变更路径（diffProfileDraft），卡片用于展示影响面。 */
  changes: string[];
  /** 保存/校验进行中：卡片只读，避免写回中途改草稿。 */
  disabled: boolean;
  draft: ProfileDraft;
  /** 本地（纯函数）校验结果，实时随草稿变化。 */
  issues: ProfileDraftIssue[];
  /** 后端 /preview 结果：validation 卡展示生效视图与估算（形状与 GET 同构）。 */
  preview: RuntimeProfilePreviewReport | null;
  view: RuntimeProfileView;
  addOverride: () => void;
  removeOverride: (index: number) => void;
  update: <K extends keyof ProfileDraft>(key: K, value: ProfileDraft[K]) => void;
  updateOverride: (index: number, patch: Partial<ProfileDraftOverride>) => void;
};

export type ProfileValidationState = {
  isValidating: boolean;
  isPreviewing: boolean;
  /** 最近一次校验时间（本地时间字符串）；空串表示尚未校验。 */
  lastValidatedAt: string;
  preview: RuntimeProfilePreviewReport | null;
  report: RuntimeProfileValidationReport | null;
};
