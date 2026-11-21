// MessageComposer 单测共享夹具（非测试文件）：
// 挂载生命周期、props 基线、附件控制器桩与常用菜单夹具，供多个 composer 测试文件复用。
import { act, type ComponentProps, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, vi } from "vitest";

import { MessageComposer } from "@/components/workspace/message-composer";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { type ComposerCommandDefinition } from "@/lib/composer-commands";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

export type ComposerTestProps = ComponentProps<typeof MessageComposer>;

export const COMMANDS: readonly ComposerCommandDefinition[] = [
  { name: "export", kind: "action" },
  { name: "model", kind: "popupSelect" },
];

export const REFERENCE_GROUPS: readonly ComposerReferenceGroup[] = [
  {
    id: "artifacts",
    label: "文件",
    items: [
      { id: "a1", label: "app.tsx", insertText: "src/app.tsx" },
      { id: "a2", label: "report.md", insertText: "reports/report.md" },
    ],
  },
];

export function createAttachmentsStub(
  overrides: Partial<ComposerAttachmentsController> = {},
): ComposerAttachmentsController {
  const attachments = overrides.attachments ?? [];
  return {
    attachments,
    isDragOver: false,
    rejectedCount: 0,
    uploadingCount: attachments.filter((item) => item.status === "uploading")
      .length,
    uploadedCount: attachments.filter((item) => item.status === "uploaded")
      .length,
    unsettledCount: attachments.filter((item) => item.status !== "uploaded")
      .length,
    uploadedPaths: attachments
      .filter((item) => item.status === "uploaded" && item.remotePath)
      .map((item) => item.remotePath as string),
    addFiles: vi.fn(),
    removeAttachment: vi.fn(),
    retryAttachment: vi.fn(),
    clearAttachments: vi.fn(),
    acknowledgeRejections: vi.fn(),
    ...overrides,
  };
}

export function createComposerTestProps(
  overrides: Partial<ComposerTestProps> = {},
): ComposerTestProps {
  return {
    attachments: createAttachmentsStub(),
    density: "comfortable",
    draft: "",
    hasSession: false,
    isNewThread: true,
    isResponding: false,
    modelOptions: ["model-a"],
    onDraftChange: vi.fn(),
    onModelChange: vi.fn(),
    onProviderChange: vi.fn(),
    onReasoningEffortChange: vi.fn(),
    onStop: vi.fn(),
    onSubmit: vi.fn(),
    providerOptions: ["provider-a"],
    reasoningEffortDefault: "",
    reasoningEffortError: null,
    reasoningEffortOptions: [],
    runtimeModelsError: null,
    runtimeModelsLoading: false,
    selectedModel: "model-a",
    selectedProvider: "provider-a",
    selectedReasoningEffort: "",
    ...overrides,
  };
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

export function installComposerTestLifecycle(): void {
  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container?.remove();
    container = null;
    root = null;
    document.body.innerHTML = "";
    Reflect.deleteProperty(HTMLTextAreaElement.prototype, "scrollHeight");
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });
}

export function composerTestContainer(): HTMLDivElement {
  if (!container) {
    throw new Error("composer test container is not mounted");
  }
  return container;
}

export function renderInComposerRoot(element: ReactElement): void {
  act(() => root?.render(element));
}

export function renderComposerElement(
  props: ComposerTestProps,
): void {
  renderInComposerRoot(<MessageComposer {...props} />);
}