import { describe, expect, it } from "vitest";

import {
  addShellPreferItem,
  formatShellPrefer,
  getRuntimeShellConfig,
  moveShellPreferItem,
  parseShellPreferInput,
  removeShellPreferItem,
  runtimeShellCandidateIds,
} from "./runtime-shell-domain-utils";

describe("getRuntimeShellConfig", () => {
  it("treats a missing shell section as the builtin chain with auto-detect on", () => {
    const config = getRuntimeShellConfig({ providers: {} });

    expect(config.autoDetect).toBe(true);
    expect(config.strictProbe).toBe(true);
    expect(config.prefer).toEqual([]);
    expect(config.path).toBe("");
    expect(config.scriptDelivery).toBe("stdin");
  });

  it("reads explicit values and keeps unknown extra keys out of the summary", () => {
    const config = getRuntimeShellConfig({
      shell: {
        autoDetect: false,
        strictProbe: false,
        prefer: ["git-bash", " "],
        path: " C:\\msys64\\usr\\bin\\bash.exe ",
        probeTimeout: 4000,
        scriptDelivery: "argv",
        env: { MSYS_NO_PATHCONV: "1" },
      },
    });

    expect(config.autoDetect).toBe(false);
    expect(config.strictProbe).toBe(false);
    // 空白项被丢掉，但值不被 trim —— 路径由后端负责规范化。
    expect(config.prefer).toEqual(["git-bash"]);
    expect(config.path).toBe(" C:\\msys64\\usr\\bin\\bash.exe ");
    expect(config.probeTimeout).toBe("4000");
    expect(config.scriptDelivery).toBe("argv");
    expect(config.env).toEqual({ MSYS_NO_PATHCONV: "1" });
  });

  it("accepts a comma separated string for prefer", () => {
    const config = getRuntimeShellConfig({ shell: { prefer: "busybox, pwsh" } });

    expect(config.prefer).toEqual(["busybox", "pwsh"]);
  });

  it("falls back to stdin when scriptDelivery is blank", () => {
    expect(getRuntimeShellConfig({ shell: { scriptDelivery: "  " } }).scriptDelivery).toBe(
      "stdin",
    );
  });
});

describe("parseShellPreferInput", () => {
  it("splits on commas, spaces and full-width commas, deduplicating while keeping order", () => {
    expect(parseShellPreferInput("Git-Bash, busybox，git-bash  pwsh")).toEqual([
      "git-bash",
      "busybox",
      "pwsh",
    ]);
  });

  it("returns an empty list for blank input", () => {
    expect(parseShellPreferInput("   ,  ")).toEqual([]);
  });
});

describe("formatShellPrefer", () => {
  it("joins ids with a comma and space", () => {
    expect(formatShellPrefer(["git-bash", "pwsh"])).toBe("git-bash, pwsh");
    expect(formatShellPrefer([])).toBe("");
  });
});

describe("moveShellPreferItem", () => {
  it("swaps with the neighbour", () => {
    expect(moveShellPreferItem(["a", "b", "c"], 1, -1)).toEqual(["b", "a", "c"]);
    expect(moveShellPreferItem(["a", "b", "c"], 1, 1)).toEqual(["a", "c", "b"]);
  });

  it("is a no-op when the move would fall off either end", () => {
    const prefer = ["a", "b"];
    expect(moveShellPreferItem(prefer, 0, -1)).toBe(prefer);
    expect(moveShellPreferItem(prefer, 1, 1)).toBe(prefer);
    expect(moveShellPreferItem(prefer, 9, 1)).toBe(prefer);
    expect(moveShellPreferItem(prefer, -1, 1)).toBe(prefer);
  });
});

describe("addShellPreferItem / removeShellPreferItem", () => {
  it("normalizes case and skips duplicates", () => {
    const prefer = ["git-bash"];
    expect(addShellPreferItem(prefer, "  PWSH ")).toEqual(["git-bash", "pwsh"]);
    expect(addShellPreferItem(prefer, "git-bash")).toBe(prefer);
    expect(addShellPreferItem(prefer, "  ")).toBe(prefer);
  });

  it("removes by exact id", () => {
    expect(removeShellPreferItem(["git-bash", "pwsh"], "pwsh")).toEqual(["git-bash"]);
    expect(removeShellPreferItem(["git-bash"], "wsl")).toEqual(["git-bash"]);
  });
});

describe("runtimeShellCandidateIds", () => {
  it("never lists the wsl stub first — the builtin chain starts with git-bash", () => {
    expect(runtimeShellCandidateIds[0]).toBe("git-bash");
    expect(new Set(runtimeShellCandidateIds).size).toBe(runtimeShellCandidateIds.length);
  });
});
