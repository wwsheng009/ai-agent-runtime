// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import type { RuntimeSessionCheckpointPreviewMode } from "@/lib/runtime-api";

import { ArtifactPanelCheckpointDetailSection } from "./checkpoint-detail-section";

// 用户诉求：点「还原模式」里的选项只能切换选择，必须再点确认按钮才会执行还原。
// 这三条断言把这个契约锁死，避免回归成「点选项即还原」。
describe("ArtifactPanelCheckpointDetailSection restore modes", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let onRestoreCheckpoint: Mock<(mode?: RuntimeSessionCheckpointPreviewMode) => void>;

  type ReactActEnvironmentGlobal = typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean;
  };

  const selectedCheckpoint = {
    id: "ckpt-1234567890ab",
    session_id: "session-1",
    message_count: 4,
    conversation_exact: true,
    created_at: new Date("2026-01-01T00:00:00.000Z").toISOString(),
  };

  const render = () => {
    act(() => {
      root = createRoot(container);
      root.render(
        <ArtifactPanelCheckpointDetailSection
          checkpointConversationSummary={[]}
          checkpointDetailLoading={false}
          checkpointDetailsError={null}
          checkpointFileCode={{ code: "", language: "text", title: "" }}
          checkpointFilesForSelection={[]}
          checkpointProvenance={[]}
          checkpointProvenanceSummary={[]}
          checkpointRestoreError={null}
          checkpointRestorePendingId=""
          checkpointRestoreSummary={null}
          onRestoreCheckpoint={onRestoreCheckpoint}
          onSelectCheckpointFile={() => {}}
          selectedCheckpoint={selectedCheckpoint}
          selectedCheckpointFilePath={null}
        />,
      );
    });
  };

  const restoreModeInputs = () =>
    Array.from(
      container.querySelectorAll<HTMLInputElement>('input[name="checkpoint-restore-mode"]'),
    );

  const confirmButton = () =>
    container.querySelector<HTMLButtonElement>('button[aria-label="确认还原"]');

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    onRestoreCheckpoint = vi.fn<(mode?: RuntimeSessionCheckpointPreviewMode) => void>();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("默认选中全部还原，且仅切换选项不会触发还原", () => {
    render();

    const inputs = restoreModeInputs();
    expect(inputs).toHaveLength(3);
    expect(inputs.find((input) => input.checked)?.value).toBe("both");

    const filesOnly = inputs.find((input) => input.value === "code");
    expect(filesOnly).toBeTruthy();
    act(() => {
      filesOnly?.click();
    });

    expect(onRestoreCheckpoint).not.toHaveBeenCalled();
    expect(restoreModeInputs().find((input) => input.checked)?.value).toBe("code");
  });

  it("点确认按钮才执行还原，并带上选中的模式", () => {
    render();

    const conversationOnly = restoreModeInputs().find(
      (input) => input.value === "conversation",
    );
    act(() => {
      conversationOnly?.click();
    });
    expect(onRestoreCheckpoint).not.toHaveBeenCalled();

    act(() => {
      confirmButton()?.click();
    });
    expect(onRestoreCheckpoint).toHaveBeenCalledTimes(1);
    expect(onRestoreCheckpoint).toHaveBeenCalledWith("conversation");
  });

  it("还原在途时禁用选项与确认按钮", () => {
    act(() => {
      root = createRoot(container);
      root.render(
        <ArtifactPanelCheckpointDetailSection
          checkpointConversationSummary={[]}
          checkpointDetailLoading={false}
          checkpointDetailsError={null}
          checkpointFileCode={{ code: "", language: "text", title: "" }}
          checkpointFilesForSelection={[]}
          checkpointProvenance={[]}
          checkpointProvenanceSummary={[]}
          checkpointRestoreError={null}
          checkpointRestorePendingId={selectedCheckpoint.id}
          checkpointRestoreSummary={null}
          onRestoreCheckpoint={onRestoreCheckpoint}
          onSelectCheckpointFile={() => {}}
          selectedCheckpoint={selectedCheckpoint}
          selectedCheckpointFilePath={null}
        />,
      );
    });

    expect(restoreModeInputs().every((input) => input.disabled)).toBe(true);
    expect(confirmButton()?.disabled).toBe(true);
    expect(container.textContent).toContain("还原中");
  });

  it("还原成功后展示带插值的本地化摘要", () => {
    act(() => {
      root = createRoot(container);
      root.render(
        <ArtifactPanelCheckpointDetailSection
          checkpointConversationSummary={[]}
          checkpointDetailLoading={false}
          checkpointDetailsError={null}
          checkpointFileCode={{ code: "", language: "text", title: "" }}
          checkpointFilesForSelection={[]}
          checkpointProvenance={[]}
          checkpointProvenanceSummary={[]}
          checkpointRestoreError={null}
          checkpointRestorePendingId=""
          checkpointRestoreSummary={{
            checkpointId: "ckpt-1234567890ab",
            mode: "code",
            appliedPaths: 3,
            conversationChanged: false,
          }}
          onRestoreCheckpoint={onRestoreCheckpoint}
          onSelectCheckpointFile={() => {}}
          selectedCheckpoint={selectedCheckpoint}
          selectedCheckpointFilePath={null}
        />,
      );
    });

    const summary = container.textContent ?? "";
    expect(summary).toContain("已还原检查点 ckpt-1234567");
    expect(summary).toContain("还原文件");
    expect(summary).toContain("文件 3 项");
    expect(summary).toContain("未变更");
  });
});
