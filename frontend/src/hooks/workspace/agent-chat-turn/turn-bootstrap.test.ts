// P2：`/skill` 回合化的请求体映射单测。
//
// 覆盖 turn-bootstrap 的 wire 契约：`expose_skills` 仅在提供非空名单时出现，字段名
// 保持 snake_case（后端 `/api/agent/chat` 按 json tag 解析）；缺省不改变既有请求体。
// 字段随后由 `streamAgentChat` JSON.stringify 原样随 POST 发出。

import { describe, expect, it } from "vitest";

import { DEFAULT_APP_SETTINGS } from "@/core/settings/local";
import type { Thread } from "@/data/mock";
import { prepareAgentChatTurn } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";

function makeThread(): Thread {
  return {
    id: "thread-1",
    title: "Skill turn",
    summary: "",
    updatedAt: "2026-09-18T00:00:00.000Z",
    status: "active",
    transport: "runtime",
    runtimeEventCount: 0,
    lastError: null,
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
    sessionId: "session-1",
  } as unknown as Thread;
}

function prepare(
  exposeSkills?: readonly string[],
  overrides: {
    selectedThread?: Thread;
    profile?: string;
    permissionMode?: string;
    permissionModeConfirm?: boolean;
    workspacePath?: string;
  } = {},
) {
  return prepareAgentChatTurn({
    prompt: "pwd",
    selectedThread: overrides.selectedThread ?? makeThread(),
    trajectoryStore: {} as TrajectoryStore,
    selectedProvider: "deepseek",
    selectedModel: "deepseek-chat",
    selectedReasoningEffort: "medium",
    settings: DEFAULT_APP_SETTINGS,
    workspacePath: overrides.workspacePath,
    ...(exposeSkills ? { exposeSkills } : {}),
    profile: overrides.profile,
    permissionMode: overrides.permissionMode,
    permissionModeConfirm: overrides.permissionModeConfirm,
  });
}

describe("prepareAgentChatTurn / expose_skills 映射", () => {
  it("普通回合不携带 expose_skills（字段缺省，不改变既有请求体）", () => {
    const { requestPayload } = prepare();

    expect("expose_skills" in requestPayload).toBe(false);
    expect(requestPayload.messages).toEqual([{ role: "user", content: "pwd" }]);
  });

  it("/skill 回合携带 snake_case expose_skills 名单，prompt 为用户输入", () => {
    const { requestPayload } = prepare(["run_shell_command"]);

    expect(requestPayload.expose_skills).toEqual(["run_shell_command"]);
    expect(requestPayload.messages).toEqual([{ role: "user", content: "pwd" }]);
    // body 序列化后字段名必须是 snake_case（后端只认该 wire 契约）。
    expect(JSON.parse(JSON.stringify(requestPayload))).toMatchObject({
      expose_skills: ["run_shell_command"],
    });
  });

  it("空名单按未提供处理（不发空数组）", () => {
    const { requestPayload } = prepare([]);

    expect("expose_skills" in requestPayload).toBe(false);
  });

  it("首轮新线程携带 profile、权限模式与显式危险模式确认", () => {
    const { requestPayload } = prepare(undefined, {
      selectedThread: { ...makeThread(), id: NEW_THREAD_ID },
      profile: "reviewer",
      permissionMode: "bypass_permissions",
      permissionModeConfirm: true,
    });

    expect(requestPayload).toMatchObject({
      profile: "reviewer",
      permission_mode: "bypass_permissions",
      permission_mode_confirm: true,
    });
  });

  it("仅在确认值为 true 时发送危险权限模式确认", () => {
    const selectedThread = { ...makeThread(), id: NEW_THREAD_ID };
    const confirmed = prepare(undefined, {
      selectedThread,
      permissionMode: "bypass_permissions",
      permissionModeConfirm: true,
    }).requestPayload;
    const unconfirmed = prepare(undefined, {
      selectedThread,
      permissionMode: "bypass_permissions",
      permissionModeConfirm: false,
    }).requestPayload;

    expect(confirmed.permission_mode_confirm).toBe(true);
    expect("permission_mode_confirm" in unconfirmed).toBe(false);
  });

  it("已有会话不发送首轮 profile 或权限模式覆盖", () => {
    const { requestPayload } = prepare(undefined, {
      selectedThread: makeThread(),
      profile: "reviewer",
      permissionMode: "accept_edits",
      permissionModeConfirm: true,
    });

    expect("profile" in requestPayload).toBe(false);
    expect("permission_mode" in requestPayload).toBe(false);
    expect("permission_mode_confirm" in requestPayload).toBe(false);
  });

  it("新会话首轮使用选中的工作区路径，已有会话不接受工作区覆盖", () => {
    const newThread = prepare(undefined, {
      selectedThread: { ...makeThread(), id: NEW_THREAD_ID },
      workspacePath: "E:/projects/demo",
    }).requestPayload;
    const existingThread = prepare(undefined, {
      workspacePath: "E:/projects/other",
    }).requestPayload;

    expect(newThread.workspace_path).toBe("E:/projects/demo");
    expect(existingThread.workspace_path).toBeUndefined();
  });
});
