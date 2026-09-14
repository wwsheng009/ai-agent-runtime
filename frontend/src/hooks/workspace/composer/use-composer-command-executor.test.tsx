// @vitest-environment jsdom

// P2-7：composer 内置命令执行器单测。
//
// 覆盖口径：认领语义（未认领交回 composer）/ 参数校验前置失败 / 成功与失败通知 /
// 错误隔离（一条命令失败不影响后续命令）。
//
// 注意：`notice` 是渲染快照的一部分，断言一律读**最近一次渲染**的控制器（`current()`），
// 而不是发起命令时那个旧引用。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { exportSessionTrajectoryJsonlMock } = vi.hoisted(() => ({
  exportSessionTrajectoryJsonlMock: vi.fn(),
}));

vi.mock("@/lib/trajectory/export-session", () => ({
  exportSessionTrajectoryJsonl: exportSessionTrajectoryJsonlMock,
}));

import {
  useComposerCommandExecutor,
  type ComposerCommandExecutor,
  type UseComposerCommandExecutorOptions,
} from "./use-composer-command-executor";
import { COMPOSER_BUILTIN_COMMANDS } from "@/lib/composer-builtin-commands";
import {
  createComposerCommandRegistry,
  findComposerCommand,
  type ComposerCommand,
} from "@/lib/composer-commands";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const registry = createComposerCommandRegistry(COMPOSER_BUILTIN_COMMANDS);

function command(key: string): ComposerCommand {
  const found = findComposerCommand(registry, key);
  if (!found) {
    throw new Error(`test setup: command ${key} is not registered`);
  }
  return found;
}

function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (executor: ComposerCommandExecutor) => void;
  options: UseComposerCommandExecutorOptions;
}) {
  const executor = useComposerCommandExecutor(options);
  onSnapshot(executor);
  return null;
}

describe("useComposerCommandExecutor", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: ComposerCommandExecutor | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
    exportSessionTrajectoryJsonlMock.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  /** 最近一次渲染的控制器快照（notice 随之更新）。 */
  function current(): ComposerCommandExecutor {
    if (!latest) {
      throw new Error("test setup: harness has not rendered yet");
    }
    return latest;
  }

  async function render(options: UseComposerCommandExecutorOptions) {
    await act(async () => {
      root.render(
        <Harness onSnapshot={(value) => (latest = value)} options={options} />,
      );
    });
    return current();
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  /** 派发一条命令并等状态回填。 */
  async function run(key: string, args: string) {
    let handled = false;
    await act(async () => {
      handled = current().run(command(key), args);
    });
    await flush();
    return handled;
  }

  it("未认领的命令返回 false（交回 composer 显示未接入提示）", async () => {
    await render({ sessionId: "session-1" });
    const unknown = {
      key: "feedback",
      name: "feedback",
      kind: "execute",
    } as ComposerCommand;

    let handled = true;
    await act(async () => {
      handled = current().run(unknown, "");
    });

    expect(handled).toBe(false);
    expect(current().notice).toBeNull();
  });

  it("/rename 缺标题时不调用重命名处理器，并给出可见错误", async () => {
    const onRenameSession = vi.fn().mockResolvedValue(undefined);
    await render({ onRenameSession, sessionId: "session-1" });

    const handled = await run("rename", "   ");

    expect(handled).toBe(true);
    expect(onRenameSession).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.rename.needTitle",
    });
  });

  it("/rename 调用与侧栏同一处理器，成功回填标题", async () => {
    const onRenameSession = vi.fn().mockResolvedValue(undefined);
    await render({ onRenameSession, sessionId: "session-1" });

    await run("rename", '"Renamed by command"');

    expect(onRenameSession).toHaveBeenCalledWith(
      "session-1",
      "Renamed by command",
    );
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.rename.done",
      values: { title: "Renamed by command" },
    });
  });

  it("重命名失败只产出错误通知，后续命令仍可执行（错误隔离）", async () => {
    const onRenameSession = vi.fn().mockRejectedValue(new Error("boom"));
    exportSessionTrajectoryJsonlMock.mockResolvedValue({
      eventCount: 3,
      filename: "trajectory-session-1.jsonl",
      redacted: false,
    });
    await render({ onRenameSession, sessionId: "session-1" });

    await run("rename", "next");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.rename.failed",
      values: { message: "boom" },
    });

    await run("export", "");

    expect(exportSessionTrajectoryJsonlMock).toHaveBeenCalledWith("session-1", {
      redact: false,
    });
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.export.done",
      values: { count: 3, filename: "trajectory-session-1.jsonl" },
    });
  });

  it("/export --redact 走脱敏导出并回填脱敏通知", async () => {
    exportSessionTrajectoryJsonlMock.mockResolvedValue({
      eventCount: 12,
      filename: "trajectory-session-1-redacted.jsonl",
      redacted: true,
    });
    await render({ sessionId: "session-1" });

    await run("export", "--redact");

    expect(exportSessionTrajectoryJsonlMock).toHaveBeenCalledWith("session-1", {
      redact: true,
    });
    expect(current().notice).toEqual({
      tone: "success",
      messageKey: "composer.builtin.export.doneRedacted",
      values: { count: 12, filename: "trajectory-session-1-redacted.jsonl" },
    });
  });

  it("/export 参数非法时不发起导出", async () => {
    await render({ sessionId: "session-1" });

    await run("export", "--json");
    expect(exportSessionTrajectoryJsonlMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.unknownFlag",
      values: { flag: "--json" },
    });

    await run("export", "now");
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.unexpectedArgument",
      values: { value: "now" },
    });
  });

  it("无会话 key 时如实报错，不伪造导出结果", async () => {
    await render({ sessionId: undefined });

    await run("export", "");

    expect(exportSessionTrajectoryJsonlMock).not.toHaveBeenCalled();
    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.noSession",
    });
  });

  it("导出失败回填错误通知（含原因）", async () => {
    exportSessionTrajectoryJsonlMock.mockRejectedValue(
      new Error("network down"),
    );
    await render({ sessionId: "session-1" });

    await run("export", "");

    expect(current().notice).toEqual({
      tone: "error",
      messageKey: "composer.builtin.export.failed",
      values: { message: "network down" },
    });
  });

  it("dismissNotice 清空通知后不残留", async () => {
    await render({ sessionId: "session-1" });

    await run("export", "--json");
    expect(current().notice).not.toBeNull();

    await act(async () => current().dismissNotice());
    expect(current().notice).toBeNull();
  });
});
