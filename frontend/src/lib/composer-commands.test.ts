import { describe, expect, it } from "vitest";

import {
  ComposerCommandConflictError,
  ComposerCommandNameError,
  classifyComposerSubmit,
  createComposerCommandRegistry,
  findComposerCommand,
  normalizeComposerCommandName,
  parseComposerCommandLine,
} from "./composer-commands";

const registry = createComposerCommandRegistry([
  { name: "export", kind: "action" },
  { name: "/Model", kind: "popupSelect" },
  { name: "plan" },
]);

describe("createComposerCommandRegistry", () => {
  it("normalizes leading slash and case into a single lookup key", () => {
    expect(normalizeComposerCommandName(" /Export ")).toBe("export");
    expect(findComposerCommand(registry, "/MODEL")?.kind).toBe("popupSelect");
    expect(findComposerCommand(registry, "plan")?.key).toBe("plan");
  });

  it("keeps the declared casing for display", () => {
    expect(findComposerCommand(registry, "model")?.name).toBe("Model");
  });

  it("defaults the kind to execute", () => {
    expect(findComposerCommand(registry, "plan")?.kind).toBe("execute");
  });

  it("fails loudly on an invalid name instead of dropping it", () => {
    expect(() => createComposerCommandRegistry([{ name: "/" }])).toThrow(
      ComposerCommandNameError,
    );
    expect(() => createComposerCommandRegistry([{ name: "bad name" }])).toThrow(
      ComposerCommandNameError,
    );
  });

  it("fails loudly on duplicate keys instead of silently overwriting", () => {
    expect(() =>
      createComposerCommandRegistry([{ name: "export" }, { name: "/EXPORT" }]),
    ).toThrow(ComposerCommandConflictError);
  });
});

describe("parseComposerCommandLine", () => {
  it("treats any leading-slash line as a command line", () => {
    expect(parseComposerCommandLine("/export")).toEqual({ name: "export", args: "" });
    expect(parseComposerCommandLine("/  ")).toEqual({ name: "", args: "" });
    // 名字不合法也仍是命令行：绝不退回普通消息。
    expect(parseComposerCommandLine("/not valid!")).toEqual({
      name: "not",
      args: "valid!",
    });
  });

  it("keeps raw args after the command name", () => {
    expect(parseComposerCommandLine("/export --json now")).toEqual({
      name: "export",
      args: "--json now",
    });
  });

  it("ignores slashes that are not at line start", () => {
    expect(parseComposerCommandLine("see src/app.tsx")).toBeNull();
    expect(parseComposerCommandLine("path/to/file")).toBeNull();
    expect(parseComposerCommandLine("run\n/export")).toBeNull();
  });
});

describe("classifyComposerSubmit", () => {
  it("classifies empty and prompt drafts", () => {
    expect(classifyComposerSubmit("   ", registry).kind).toBe("empty");
    expect(classifyComposerSubmit("hello", registry).kind).toBe("prompt");
  });

  it("resolves a known command with args", () => {
    const classification = classifyComposerSubmit("/export now", registry);
    expect(classification.kind).toBe("command");
    if (classification.kind === "command") {
      expect(classification.command.key).toBe("export");
      expect(classification.args).toBe("now");
    }
  });

  it("never degrades a command line into a prompt", () => {
    expect(classifyComposerSubmit("/", registry).kind).toBe("incomplete-command");
    expect(classifyComposerSubmit("/nope", registry)).toEqual({
      kind: "unknown-command",
      name: "nope",
    });
  });
});
