// P2-7：composer 内置命令执行器。
//
// 契约：
// - `run` 是同步派发：返回 true 表示本执行器认领了该命令（结果异步回填通知），
//   返回 false 表示未认领，交回 composer 显示「尚未接入执行器」——绝不静默降级为 prompt；
// - **错误隔离**：每条命令独立 try/catch，失败只产出自己的错误通知，
//   不影响后续命令、也不影响输入框与发送路径；
// - 结果通知只持 i18n key 与插值（模型层不持文案），渲染层本地化。
import { useCallback, useRef, useState } from "react";

import {
  parseExportCommandArgs,
  parseRenameCommandArgs,
} from "@/lib/composer-builtin-commands";
import type { ComposerCommand } from "@/lib/composer-commands";
import { exportSessionTrajectoryJsonl } from "@/lib/trajectory/export-session";

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
};

function describeError(error: unknown): string {
  return error instanceof Error && error.message.trim().length > 0
    ? error.message
    : String(error);
}

export function useComposerCommandExecutor({
  sessionId,
  onRenameSession,
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
            values: { count: result.eventCount, filename: result.filename },
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

  const run = useCallback(
    (command: ComposerCommand, args: string): boolean => {
      switch (command.key) {
        case "export":
          runExport(args);
          return true;
        case "rename":
          runRename(args);
          return true;
        default:
          // 未认领：composer 会显示 no-executor 提示，而不是把命令行当消息发出去。
          return false;
      }
    },
    [runExport, runRename],
  );

  const dismissNotice = useCallback(() => setNotice(null), []);

  return { run, notice, dismissNotice };
}
