// composer-menu 测试共享夹具：命令注册表、引用组与 source 构造器。

import { createComposerCommandRegistry } from "./composer-commands";
import type { ComposerMenuSource, ComposerReferenceGroup } from "./composer-menu";

export const commands = createComposerCommandRegistry([
  { name: "export", kind: "action", descriptionKey: "composer.menu.commands" },
  { name: "plan" },
]).commands;

export const referenceGroups: ComposerReferenceGroup[] = [
  {
    id: "artifacts",
    label: "文件",
    items: [
      { id: "a1", label: "app.tsx", insertText: "src/app.tsx" },
      { id: "a2", label: "my report.md", insertText: "my report.md" },
    ],
  },
  { id: "empty", label: "空组", items: [] },
];

export function source(
  overrides: Partial<ComposerMenuSource> = {},
): ComposerMenuSource {
  return {
    mode: "all",
    level: { kind: "root" },
    query: "",
    commands,
    referenceGroups,
    hasAttachAction: true,
    attachLabel: "添加附件",
    ...overrides,
  };
}