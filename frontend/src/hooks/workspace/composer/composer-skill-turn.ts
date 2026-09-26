// P2-7（第 14 批）/ S5：composer `/skill` 回合化构造器。
//
// 从 `workspace-shell/main-section.tsx` 机械抽出（该文件受 P0-2 的 500 非空行门禁约束），
// 仅搬迁不改语义：把 skill 名与用户 prompt 转成普通回合提交（`submitPrompt` 的 options
// 形态）并声明 expose_skills；用户消息写成 `/skill <name> <args>`，线程里能直接看出
// 这是一次 skill 调用。前置条件不满足（提交被拦下）时抛错，由执行器如实回执。

import { composerSkillTurnPrompt } from "@/lib/composer-skill-options";

import type { ComposerSkillTurnRunner } from "./use-composer-command-executor";
import type { AgentChatSubmitOptions } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";

export function createComposerSkillTurnRunner(
  onSubmit: (options?: AgentChatSubmitOptions) => boolean | void,
): ComposerSkillTurnRunner {
  return (skillName, prompt) => {
    if (
      onSubmit({
        prompt: composerSkillTurnPrompt(skillName, prompt),
        exposeSkills: [skillName],
      }) === false
    ) {
      throw new Error(`skill turn not started: ${skillName}`);
    }
  };
}
