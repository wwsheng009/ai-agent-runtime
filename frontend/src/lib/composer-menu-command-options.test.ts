import { describe, expect, it } from "vitest";

import { createComposerCommandRegistry } from "./composer-commands";
import {
  buildComposerMenu,
  type ComposerCommandOptionsSource,
  type ComposerMenuSource,
} from "./composer-menu";
import { source } from "./composer-menu.test-fixtures";

describe("composer command option level", () => {
  const modelCommands = createComposerCommandRegistry([
    {
      name: "model",
      kind: "popupSelect",
      options: [{ value: "gpt-5", label: "gpt-5", description: "openai" }],
    },
  ]).commands;

  const optionsSource: ComposerCommandOptionsSource = {
    commandKey: "model",
    commandName: "model",
    label: "model",
    options: [
      {
        value: "deepseek-chat",
        label: "deepseek-chat",
        description: "deepseek",
      },
      { value: "gpt-5", label: "gpt-5", description: "openai" },
      { value: "gpt-5-mini", label: "gpt-5-mini", description: "openai" },
    ],
  };

  function optionSource(
    overrides: Partial<ComposerMenuSource> = {},
  ): ComposerMenuSource {
    return source({
      mode: "all",
      level: { kind: "command-options", commandKey: "model" },
      commands: modelCommands,
      commandOptions: optionsSource,
      ...overrides,
    });
  }

  it("候选独占一层：不混入命令 / 引用 / 附件，动作携带命令名与参数原文", () => {
    const snapshot = buildComposerMenu(optionSource());

    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "command-options:model",
    ]);
    expect(snapshot.items.map((item) => item.label)).toEqual([
      "deepseek-chat",
      "gpt-5",
      "gpt-5-mini",
    ]);
    expect(snapshot.items[0].description).toBe("deepseek");
    expect(snapshot.items[0].action).toEqual({
      kind: "command-option",
      name: "model",
      value: "deepseek-chat",
    });
  });

  it("按 query 过滤候选，全落空视为空快照", () => {
    expect(
      buildComposerMenu(optionSource({ query: "gpt" })).items.map(
        (item) => item.label,
      ),
    ).toEqual(["gpt-5", "gpt-5-mini"]);

    const empty = buildComposerMenu(optionSource({ query: "zzz" }));
    expect(empty.empty).toBe(true);
    expect(empty.items).toEqual([]);
  });

  it("缺候选来源或候选为空时按无候选处理（不伪造占位项）", () => {
    const missing = buildComposerMenu(optionSource({ commandOptions: null }));
    expect(missing.groups).toEqual([]);
    expect(missing.items).toEqual([]);
    expect(missing.empty).toBe(true);

    const blank = buildComposerMenu(
      optionSource({ commandOptions: { ...optionsSource, options: [] } }),
    );
    expect(blank.groups).toEqual([]);
    expect(blank.empty).toBe(true);
  });
});