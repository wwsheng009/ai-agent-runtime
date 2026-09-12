// 由 components/workspace/runtime-teams/use-runtime-team-dispatch.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  normalizeTaskTitle,
  parsePathLines,
  resolveDispatchRolePlan,
  uniqueStrings,
  type DispatchTemplateMode,
} from "@/components/workspace/runtime-teams/shared";

import type {
  DispatchTaskDraftState,
  DispatchTaskRequest,
  DispatchTeamIdentifier,
} from "./types";

export function buildDispatchTaskRequest(
  drafts: DispatchTaskDraftState,
): { error: string | null; request: DispatchTaskRequest | null } {
  const goal = drafts.goalDraft.trim();
  const title = normalizeTaskTitle(drafts.titleDraft, goal);
  if (!goal && !title) {
    return {
      error: "enter a task title or goal before dispatching",
      request: null,
    };
  }

  const priority = Number.parseInt(drafts.priorityDraft, 10);
  return {
    error: null,
    request: {
      deliverables: parsePathLines(drafts.deliverablesDraft),
      goal: goal || title,
      inputs: parsePathLines(drafts.inputsDraft),
      priority: Number.isNaN(priority) ? 50 : priority,
      status: "ready",
      title,
    },
  };
}

export function resolveSelectedDispatchTeamIds(
  current: string[],
  teams: DispatchTeamIdentifier[],
): string[] {
  const availableIds = new Set(teams.map((team) => team.id));
  const filtered = current.filter((id) => availableIds.has(id));
  if (filtered.length > 0) {
    return filtered;
  }

  return teams.map((team) => team.id);
}

export function buildRoleAwareDispatchRequest(
  baseRequest: DispatchTaskRequest,
  mode: DispatchTemplateMode,
  index: number,
) {
  const role = resolveDispatchRolePlan(mode, index);
  if (mode === "mirror") {
    return {
      request: baseRequest,
      role,
    };
  }

  return {
    request: {
      ...baseRequest,
      deliverables: uniqueStrings([
        ...(baseRequest.deliverables ?? []),
        ...role.deliverables,
      ]),
      goal: uniqueStrings([baseRequest.goal, role.goalInstruction || ""]).join("\n\n"),
      inputs: uniqueStrings([...(baseRequest.inputs ?? []), ...role.inputHints]),
      title: `${role.label}: ${baseRequest.title}`,
    },
    role,
  };
}
