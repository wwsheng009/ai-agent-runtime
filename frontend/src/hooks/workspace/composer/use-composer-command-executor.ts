// P2-7：composer 内置命令执行器。
//
// 契约：
// - `run` 是同步派发：返回 true 表示本执行器认领了该命令（结果异步回填通知），
//   返回 false 表示未认领，交回 composer 显示「尚未接入执行器」——绝不静默降级为 prompt；
// - **错误隔离**：每条命令独立 try/catch，失败只产出自己的错误通知，
//   不影响后续命令、也不影响输入框与发送路径；
// - 结果通知只持 i18n key 与插值（模型层不持文案），渲染层本地化。
import { useCallback, useRef, useState } from "react";

import { executeSkill } from "@/api/runtime/skills";
import { createLogger } from "@/core/logger";
import {
  parseExportCommandArgs,
  parseFeedbackCommandArgs,
  parseRenameCommandArgs,
} from "@/lib/composer-builtin-commands";
import type { ComposerCommand } from "@/lib/composer-commands";
import {
  exportSessionTrajectoryJsonl,
  type SessionTrajectoryExportResult,
} from "@/lib/trajectory/export-session";

const logger = createLogger("use-composer-command-executor");

export type ComposerCommandResultNotice = {
  /** 语气：错误用 role="alert"，成功用 role="status"。 */
  tone: "success" | "error";
  /** i18n key（workspace 命名空间）。 */
  messageKey: string;
  values?: Record<string, string | number>;
};

export type ComposerCommandExecutor = {
  run: (command: ComposerCommand, args: string) => boolean;
  notice: ComposerCommandResultNotice | null;
  dismissNotice: () => void;
};

export type UseComposerCommandExecutorOptions = {
  /** 当前会话；新会话尚未登记时为 undefined（导出无数据源，如实报错不伪造）。 */
  sessionId?: string;
  /** 会话重命名；与侧栏重命名同一处理器（单一事实源）。 */
  onRenameSession?: (sessionId: string, title: string) => Promise<void>;
  /** `/model`：宿主目录与「应用模型 / 打开弹窗」动作；缺省时命令如实报不可用。 */
  modelSelection?: ComposerModelSelectionBridge;
  /** `/skill`：技能名称列表与「打开弹窗」动作；缺省时命令如实报不可用。 */
  skillNames?: string[];
  openSkillDialog?: () => void;
  /**
   * P2：`/skill <name> <prompt>` 提交为普通对话回合（宿主在请求体携带
   * `expose_skills`），消息流可见。缺省时回退到 executeSkill REST
   * （无回合提交能力的宿主，例如 admin/debug 场景）。
   */
  onRunSkillTurn?: ComposerSkillTurnRunner;
};

export type ComposerSkillTurnRunner = (
  skillName: string,
  prompt: string,
) => void | Promise<void>;

export type ComposerModelSelectionBridge = {
  /** 目录内真实存在的模型 id；空数组 = 目录未就绪（不按「不存在」处理）。 */
  modelIds: readonly string[];
  /** 应用模型；与 composer 常驻座位同一处理器（单一事实源，不另建选择通道）。 */
  applyModel: (modelId: string) => void;
  /** 打开 `/model` 弹窗（无参数提交时使用）。 */
  openDialog: () => void;
};

function describeError(error: unknown): string {
  return error instanceof Error && error.message.trim().length > 0
    ? error.message
    : String(error);
}

// log-only：本仓无反馈后端路由与外部渠道，反馈只写本地结构化日志，不上报。
const feedbackLog = createLogger("composer.feedback");

/** 模型 id 解析：精确优先；否则唯一的大小写不敏感匹配；歧义 / 未命中返回 null。 */
function resolveModelId(
  modelIds: readonly string[],
  requested: string,
): string | null {
  if (modelIds.includes(requested)) {
    return requested;
  }
  const lowered = requested.toLowerCase();
  const matches = modelIds.filter((modelId) => modelId.toLowerCase() === lowered);
  return matches.length === 1 ? matches[0] : null;
}

/**
 * 导出文件行数：`historyRowCount` 由轨迹导出实现按需提供（无内容帧的会话把
 * 持久化历史投影成导出行）。该字段是增量能力——旧实现不提供时按 0 计，
 * 计数只可能小于文件行数，不虚增。
 */
function exportRowCount(result: SessionTrajectoryExportResult): number {
  const historyRowCount = (
    result as SessionTrajectoryExportResult & { historyRowCount?: number }
  ).historyRowCount;
  return (
    result.eventCount + (typeof historyRowCount === "number" ? historyRowCount : 0)
  );
}

export function useComposerCommandExecutor({
  sessionId,
  onRenameSession,
  modelSelection,
  skillNames = [],
  openSkillDialog,
  onRunSkillTurn,
}: UseComposerCommandExecutorOptions): ComposerCommandExecutor {
  const [notice, setNotice] = useState<ComposerCommandResultNotice | null>(null);
  const exportingRef = useRef(false);

  const runExport = useCallback(
    (args: string) => {
      const parsed = parseExportCommandArgs(args);
      if (!parsed.ok) {
        setNotice(
          parsed.error.kind === "unknown-flag"
            ? {
                tone: "error",
                messageKey: "composer.builtin.export.unknownFlag",
                values: { flag: parsed.error.flag },
              }
            : {
                tone: "error",
                messageKey: "composer.builtin.export.unexpectedArgument",
                values: { value: parsed.error.value },
              },
        );
        return;
      }
      if (!sessionId) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.export.noSession",
        });
        return;
      }
      if (exportingRef.current) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.export.inProgress",
        });
        return;
      }
      exportingRef.current = true;
      void (async () => {
        try {
          const result = await exportSessionTrajectoryJsonl(sessionId, {
            redact: parsed.args.redact,
          });
          setNotice({
            tone: "success",
            messageKey: result.redacted
              ? "composer.builtin.export.doneRedacted"
              : "composer.builtin.export.done",
            // 无内容帧的会话导出的是「生命周期事件 + 历史兜底行」，
            // 计数要包含兜底行，否则提示条数会小于文件里的行数。
            values: {
              count: exportRowCount(result),
              filename: result.filename,
            },
          });
        } catch (error) {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.export.failed",
            values: { message: describeError(error) },
          });
        } finally {
          exportingRef.current = false;
        }
      })();
    },
    [sessionId],
  );

  const runRename = useCallback(
    (args: string) => {
      const parsed = parseRenameCommandArgs(args);
      if (!parsed.ok) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.rename.needTitle",
        });
        return;
      }
      if (!sessionId || !onRenameSession) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.rename.unavailable",
        });
        return;
      }
      void (async () => {
        try {
          await onRenameSession(sessionId, parsed.title);
          setNotice({
            tone: "success",
            messageKey: "composer.builtin.rename.done",
            values: { title: parsed.title },
          });
        } catch (error) {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.rename.failed",
            values: { message: describeError(error) },
          });
        }
      })();
    },
    [onRenameSession, sessionId],
  );

  // log-only：写本地结构化日志并以回执如实说明「未上报」，不制造已提交的假象。
  const runFeedback = useCallback(
    (args: string) => {
      const parsed = parseFeedbackCommandArgs(args);
      if (!parsed.ok) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.feedback.needText",
        });
        return;
      }
      feedbackLog.info("composer feedback recorded (log-only, no remote channel)", {
        sessionId: sessionId ?? null,
        message: parsed.message,
      });
      setNotice({
        tone: "success",
        messageKey: "composer.builtin.feedback.recorded",
      });
    },
    [sessionId],
  );

  const runModel = useCallback(
    (args: string) => {
      const requested = args.trim();
      if (requested.length === 0) {
        // 无参数：打开 `/model` 弹窗（与常驻座位同一目录、同一处理器）。
        if (!modelSelection) {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.model.unavailable",
          });
          return;
        }
        modelSelection.openDialog();
        return;
      }
      if (!modelSelection || modelSelection.modelIds.length === 0) {
        // 目录未就绪（拉取中 / 失败 / 未配置）：不按「模型不存在」报，避免误导。
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.model.unavailable",
        });
        return;
      }
      const modelId = resolveModelId(modelSelection.modelIds, requested);
      if (!modelId) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.model.notFound",
          values: { model: requested },
        });
        return;
      }
      modelSelection.applyModel(modelId);
      setNotice({
        tone: "success",
        messageKey: "composer.builtin.model.applied",
        values: { model: modelId },
      });
    },
    [modelSelection],
  );

  const runSkill = useCallback(
    async (args: string) => {
      const trimmedArgs = args.trim();
      if (trimmedArgs.length === 0) {
        // 无参数，打开弹窗
        if (openSkillDialog) {
          openSkillDialog();
        } else {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.skill.needName",
          });
        }
        return;
      }
      
      // 有参数，解析 skill 名称和用户 prompt
      // 格式: /skill skillName [user prompt]
      const parts = trimmedArgs.split(/\s+/);
      const skillName = parts[0];
      const userPrompt = parts.slice(1).join(" ");
      
      if (skillNames.length === 0) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.skill.notAvailable",
        });
        return;
      }
      
      // 检查 skill 是否存在
      if (!skillNames.includes(skillName)) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.skill.notFound",
          values: { skill: skillName },
        });
        return;
      }

      // 回合 prompt 不能为空：保持「打开弹窗 / 提示补充」语义，不伪造空回合。
      if (userPrompt.length === 0) {
        if (openSkillDialog) {
          openSkillDialog();
        } else {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.skill.needPrompt",
            values: { skill: skillName },
          });
        }
        return;
      }

      if (onRunSkillTurn) {
        try {
          // P2 回合化：宿主提交普通回合并在请求体携带 expose_skills；成功路径不
          // 显示回执（线程里已有真实消息），避免「执行成功」提示条与消息流重复。
          await onRunSkillTurn(skillName, userPrompt);
          setNotice(null);
        } catch (error) {
          logger.error("skill turn submission failed", { skillName, error });
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.skill.failed",
            values: { skill: skillName },
          });
        }
        return;
      }

      try {
        // 回退路径（宿主未接线回合提交）：调用后端 API 执行 skill，模型驱动
        // （模型读取 skill 说明与程序清单后自行选择要调用的程序）。无消息流可
        // 承接结果，因此仍以「执行成功」回执呈现。
        await executeSkill(skillName, {
          prompt: userPrompt,
          sessionId: sessionId,
          options: { execution_mode: "model" },
        });
        
        setNotice({
          tone: "success",
          messageKey: "composer.builtin.skill.applied",
          values: { skill: skillName },
        });
      } catch (error) {
        logger.error("skill execution failed", { skillName, error });
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.skill.failed",
          values: { skill: skillName },
        });
      }
    },
    [skillNames, openSkillDialog, onRunSkillTurn, sessionId],
  );

  const run = useCallback(
    (command: ComposerCommand, args: string): boolean => {
      switch (command.key) {
        case "export":
          runExport(args);
          return true;
        case "rename":
          runRename(args);
          return true;
        case "feedback":
          runFeedback(args);
          return true;
        case "model":
          runModel(args);
          return true;
        case "skill":
          runSkill(args);
          return true;
        default:
          // 未认领：composer 会显示 no-executor 提示，而不是把命令行当消息发出去。
          return false;
      }
    },
    [runExport, runFeedback, runModel, runRename, runSkill],
  );

  const dismissNotice = useCallback(() => setNotice(null), []);

  return { run, notice, dismissNotice };
}
