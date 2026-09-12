// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 团队详情状态辅助（计数 / 空态 / 租约活跃判断 / 路径与标题归一）

import { type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";
import { type TeamDetailsState } from "@/components/workspace/runtime-teams/shared/types";

export function getSummaryCount(
  summary: RuntimeTeamSummaryEntry | undefined,
  group: "tasks" | "teammates",
  key: string,
) {
  if (!summary) {
    return 0;
  }
  return summary[group].counts?.[key] ?? 0;
}

export function createEmptyDetails(): TeamDetailsState {
  return {
    events: [],
    finalSummary: "",
    graph: null,
    mailbox: [],
    pathClaims: [],
    tasks: [],
    teammates: [],
  };
}

export function isClaimActive(leaseUntil: string | undefined) {
  if (!leaseUntil) {
    return true;
  }
  const value = new Date(leaseUntil).getTime();
  if (Number.isNaN(value)) {
    return true;
  }
  return value >= Date.now();
}

export function parsePathLines(value: string) {
  return value
    .split(/\r?\n|,/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}

export function uniqueStrings(values: string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))];
}

export function normalizeTaskTitle(title: string, goal: string) {
  const trimmedTitle = title.trim();
  if (trimmedTitle) {
    return trimmedTitle;
  }
  const trimmedGoal = goal.trim();
  if (!trimmedGoal) {
    return "";
  }
  return trimmedGoal.length > 72 ? `${trimmedGoal.slice(0, 69)}...` : trimmedGoal;
}
