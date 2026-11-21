// MessageComposer 触发菜单单测共享夹具（非测试文件）：
// 菜单动作辅助与 skill 候选夹具；受控宿主组件见 message-composer-menu-harness.tsx。
import { act, createElement } from "react";

import { ComposerMenuHarness } from "@/components/workspace/message-composer-menu-harness";
import {
  COMMANDS,
  composerTestContainer,
  createAttachmentsStub,
  createComposerTestProps,
  REFERENCE_GROUPS,
  renderInComposerRoot,
  type ComposerTestProps,
} from "@/components/workspace/message-composer-test-support";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";

export { COMMANDS, REFERENCE_GROUPS };

export const SKILL_GROUPS: readonly ComposerReferenceGroup[] = [
  {
    id: "skills",
    label: "技能",
    items: [
      {
        id: "brand",
        label: "brand",
        insertText: "brand",
        description: "design",
      },
      { id: "docx", label: "docx", insertText: "docx", description: "docs" },
    ],
    emptyText: "未找到匹配技能",
  },
];

export function renderComposer(
  overrides: Partial<ComposerTestProps> = {},
): ComposerTestProps {
  const props = createComposerTestProps({
    attachments: createAttachmentsStub(),
    commands: COMMANDS,
    hasSession: true,
    isNewThread: false,
    referenceGroups: REFERENCE_GROUPS,
    ...overrides,
  });

  renderInComposerRoot(createElement(ComposerMenuHarness, { props }));
  return props;
}

export function textarea(): HTMLTextAreaElement {
  return composerTestContainer().querySelector(
    "textarea",
  ) as HTMLTextAreaElement;
}

export function menu(): HTMLElement | null {
  return composerTestContainer().querySelector("[data-composer-menu]");
}

export function typeText(value: string) {
  const element = textarea();
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    "value",
  )?.set;
  act(() => {
    setter?.call(element, value);
    element.setSelectionRange(value.length, value.length);
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return element;
}

export function press(key: string) {
  act(() => {
    textarea().dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
    );
  });
}
