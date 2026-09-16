// 会话列表状态图标的产品口径回归：
// 2026-09-16 起「已恢复会话 / 已关闭会话」不再渲染图标（信息量低于噪声），
// 其余状态图标保持原样——避免后续改动误删归档 / 等待类等有效信号。

import { ArchiveIcon, TriangleAlertIcon } from "lucide-react";
import { describe, expect, it } from "vitest";

import {
  getRuntimeSessionActivityIcon,
  getSessionStatusIcon,
} from "./state-icon-utils";

const activityLabels = {
  sessionArchived: "已归档会话",
  sessionPending: "尚未附着运行时会话",
  sessionPlanPending: "计划待审",
  sessionRunning: "运行中",
  sessionSubagents: "子代理运行中",
  sessionWaitingAnswer: "等待回答",
  sessionWaitingApproval: "等待审批",
};

const threadSessionLabels = {
  sessionAttached: "已附着运行时会话",
  sessionError: "会话同步异常",
  sessionPending: "尚未附着运行时会话",
};

describe("会话列表状态图标口径", () => {
  it("「已恢复会话」不渲染图标", () => {
    expect(getSessionStatusIcon("restored", threadSessionLabels)).toBeNull();
  });

  it("「已关闭会话」不渲染图标", () => {
    expect(
      getRuntimeSessionActivityIcon(
        { kind: "closed", waiting: false },
        activityLabels,
      ),
    ).toBeNull();
  });

  it("归档态与等待类图标保持原样", () => {
    expect(
      getRuntimeSessionActivityIcon(
        { kind: "archived", waiting: false },
        activityLabels,
      )?.icon,
    ).toBe(ArchiveIcon);
    expect(
      getRuntimeSessionActivityIcon(
        { kind: "waitingApproval", waiting: true },
        activityLabels,
      )?.icon,
    ).toBe(TriangleAlertIcon);
  });
});
