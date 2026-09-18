// P2：`/skill` 回合的用户消息文本（线程可见形态）单测。

import { describe, expect, it } from "vitest";

import { composerSkillCommandText, composerSkillTurnPrompt } from "./composer-skill-options";

describe("composerSkillTurnPrompt", () => {
  it("保留 `/skill <name>` 调用形态，参数原样跟随", () => {
    expect(composerSkillTurnPrompt("fetch_url_content", "https://example.com")).toBe(
      "/skill fetch_url_content https://example.com",
    );
  });

  it("名称两端空白归一，与输入框回填文本一致", () => {
    expect(composerSkillTurnPrompt("  run_shell_command ", "pwd")).toBe(
      `${composerSkillCommandText("run_shell_command")}pwd`,
    );
  });
});
