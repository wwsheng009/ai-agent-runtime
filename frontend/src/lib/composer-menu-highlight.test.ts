import { describe, expect, it } from "vitest";

import {
  buildComposerMenu,
  clampComposerMenuActive,
  moveComposerMenuActive,
  resolveComposerMenuTab,
} from "./composer-menu";
import { source } from "./composer-menu.test-fixtures";

describe("composer menu highlight movement", () => {
  const items = buildComposerMenu(source({ mode: "commands" })).items;

  it("clamps a stale highlight back to the first item", () => {
    expect(clampComposerMenuActive(items, "missing")).toBe(items[0].id);
    expect(clampComposerMenuActive([], null)).toBeNull();
    expect(clampComposerMenuActive(items, items[1].id)).toBe(items[1].id);
  });

  it("moves in a ring", () => {
    expect(moveComposerMenuActive(items, items[0].id, -1)).toBe(
      items[items.length - 1].id,
    );
    expect(moveComposerMenuActive(items, items[items.length - 1].id, 1)).toBe(
      items[0].id,
    );
    expect(moveComposerMenuActive(items, null, 1)).toBe(items[0].id);
  });

  it("treats launchers as drill and leaves without a highlight as pass", () => {
    const root = buildComposerMenu(source({ mode: "references" }));
    const launcher = root.items[0];
    expect(launcher.level).toBe("launcher");
    expect(resolveComposerMenuTab(root.items, launcher.id)).toBe("drill");

    const leaf = buildComposerMenu(
      source({
        mode: "references",
        level: { kind: "group", groupId: "artifacts" },
      }),
    ).items[0];
    expect(resolveComposerMenuTab([leaf], leaf.id)).toBe("pick");
    expect(resolveComposerMenuTab(root.items, null)).toBe("pass");
    expect(resolveComposerMenuTab(root.items, "missing")).toBe("pass");
  });
});