// `/profile` 命令的执行分支（弹窗切换 + `save-as` 固化）从
// `use-composer-command-executor.ts` 拆出：P0-2 行数门禁（单文件非空行 ≤ 500），
// **行为与契约零变化**。
//
// 为什么单独成 hook：`/profile` 是唯一同时拥有「弹窗切换」与「子命令固化」两条通路
// 的命令，两段合计近百行，且都要读同一个会话身份（`sessionId`）；抽成 hook 后执行器
// 只保留 dispatch 表，命令分支各归其位（错误回执仍走同一个 `setNotice`）。
import { useCallback, useRef } from "react";

import {
  createRuntimeProfile,
  type SessionProfileSwitchReport,
} from "@/api/runtime/profiles";
import { createLogger } from "@/core/logger";
import { parseProfileSaveAsCommandArgs } from "@/lib/composer-builtin-commands";
import {
  resolveComposerProfileRef,
  type ComposerProfileCandidate,
} from "@/lib/composer-profile-options";

const logger = createLogger("use-composer-profile-command");

export type ComposerCommandResultNotice = {
  /** 语气：错误用 role="alert"，成功用 role="status"。 */
  tone: "success" | "error";
  /** i18n key（workspace 命名空间）。 */
  messageKey: string;
  values?: Record<string, string | number>;
};

export type ComposerProfileSelectionBridge = {
  /**
   * 目录候选全集（含不可解析项）：回执要能区分「不存在」与「存在但不可用」，
   * 菜单候选只用其中的 `valid` 项（见 `lib/composer-profile-options.ts`）。
   */
  candidates: readonly ComposerProfileCandidate[];
  /** 切换当前会话 profile；返回后端 Switch Report（下一轮生效语义由回执呈现）。 */
  applyProfile: (profileRef: string) => Promise<SessionProfileSwitchReport>;
  /** 打开 `/profile` 弹窗（无参数提交时使用）。 */
  openDialog: () => void;
};

/** 错误回执的统一取文：优先 Error.message，否则原样 String（不吞异常对象）。 */
export function describeError(error: unknown): string {
  return error instanceof Error && error.message.trim().length > 0
    ? error.message
    : String(error);
}

/**
 * save-as 报告面（D36）的字段计数：按值形状统计「写了哪些字段」。
 *
 * 不硬编码字段名（`tools` / `skills` / `mcp` … 由后端摘要决定），只数非空项，
 * 这样后端加字段时回执不会静默漏报，也不会因为字段改名而误报 0。
 */
function saveAsSurfaceFieldCount(surface: Record<string, unknown>): number {
  return Object.values(surface).filter((value) => {
    if (Array.isArray(value)) {
      return value.length > 0;
    }
    if (typeof value === "number") {
      return value > 0;
    }
    return value === true;
  }).length;
}

export type UseComposerProfileCommandOptions = {
  /** 当前会话；未登记时 `/profile` 与 `save-as` 都如实报「无会话」。 */
  sessionId?: string;
  /** 宿主目录与「切换 profile / 打开弹窗」动作；缺省时命令如实报不可用。 */
  profileSelection?: ComposerProfileSelectionBridge;
  /** 结果回执（执行器持有的同一 state setter，保证只有一处回执出口）。 */
  setNotice: (notice: ComposerCommandResultNotice) => void;
};

/** `/profile` 命令的运行器：返回执行器 dispatch 表要调用的 `runProfile`。 */
export function useComposerProfileCommand({
  sessionId,
  profileSelection,
  setNotice,
}: UseComposerProfileCommandOptions) {
  const savingAsRef = useRef(false);

  // `/profile save-as <名称> [--to user|project]`：把当前会话与基线 profile 的差分
  // 固化成新 profile（G1/D24）。
  //
  // 前端只是入口，差分与落盘都在后端（D36），因此回执必须如实转述后端报告：
  // 写了几个字段、哪些内容**没有**固化（prompt 等）、以及「未激活」这一事实。
  const runProfileSaveAs = useCallback(
    (raw: string) => {
      const parsed = parseProfileSaveAsCommandArgs(raw);
      if (!parsed.ok) {
        const { error } = parsed;
        setNotice(
          error.kind === "need-name"
            ? {
                tone: "error",
                messageKey: "composer.builtin.profile.saveAs.needName",
              }
            : error.kind === "unknown-flag"
              ? {
                  tone: "error",
                  messageKey: "composer.builtin.profile.saveAs.unknownFlag",
                  values: { flag: error.flag },
                }
              : error.kind === "bad-layer"
                ? {
                    tone: "error",
                    messageKey: "composer.builtin.profile.saveAs.badLayer",
                    values: { value: error.value },
                  }
                : {
                    tone: "error",
                    messageKey:
                      "composer.builtin.profile.saveAs.unexpectedArgument",
                    values: { value: error.value },
                  },
        );
        return;
      }
      if (!sessionId) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.saveAs.noSession",
        });
        return;
      }
      if (savingAsRef.current) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.saveAs.inProgress",
        });
        return;
      }
      savingAsRef.current = true;
      void (async () => {
        try {
          const result = await createRuntimeProfile({
            name: parsed.args.name,
            layer: parsed.args.layer || undefined,
            fromSession: sessionId,
          });
          setNotice({
            tone: "success",
            messageKey: "composer.builtin.profile.saveAs.done",
            values: {
              profile: result.ref || result.name,
              fields: saveAsSurfaceFieldCount(result.surface),
              omitted: result.omitted.length,
            },
          });
        } catch (error) {
          logger.error("profile save-as failed", {
            name: parsed.args.name,
            sessionId,
            error,
          });
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.profile.saveAs.failed",
            values: { message: describeError(error) },
          });
        } finally {
          savingAsRef.current = false;
        }
      })();
    },
    [sessionId, setNotice],
  );

  const runProfile = useCallback(
    (args: string) => {
      const requested = args.trim();
      // 子命令优先于「把整串当 profile 引用解析」：`save-as` 是固化，不是切换。
      if (requested === "save-as" || requested.startsWith("save-as ")) {
        runProfileSaveAs(requested.slice("save-as".length));
        return;
      }
      if (requested.length === 0) {
        // 无参数：打开 `/profile` 弹窗（候选由宿主持有，执行走同一处理器）。
        if (!profileSelection) {
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.profile.unavailable",
          });
          return;
        }
        profileSelection.openDialog();
        return;
      }
      if (!profileSelection || profileSelection.candidates.length === 0) {
        // 目录未就绪（拉取中 / 失败 / 后端未声明能力）：不按「profile 不存在」报。
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.unavailable",
        });
        return;
      }
      const candidate = resolveComposerProfileRef(
        profileSelection.candidates,
        requested,
      );
      if (!candidate) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.notFound",
          values: { profile: requested },
        });
        return;
      }
      if (!candidate.valid) {
        // 存在但解析失败：如实报「不可用 + 原因」，不伪装成「不存在」。
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.invalid",
          values: {
            profile: candidate.label,
            reason: candidate.invalidReason || candidate.ref,
          },
        });
        return;
      }
      if (!sessionId) {
        setNotice({
          tone: "error",
          messageKey: "composer.builtin.profile.noSession",
        });
        return;
      }
      void (async () => {
        try {
          const report = await profileSelection.applyProfile(candidate.ref);
          const warnings = report.warnings ?? [];
          // 回执口径（D23/D30）：切换是「下一轮生效」；在途回合存在时显式说明
          // 本回合仍走旧面；provider/model/permission 差异以 warnings 呈现
          // （只报告不隐式应用），这里把首条告警一并回显，不吞掉。
          setNotice({
            tone: "success",
            messageKey:
              warnings.length > 0
                ? "composer.builtin.profile.appliedWithWarnings"
                : report.inFlightTurn
                  ? "composer.builtin.profile.appliedAfterTurn"
                  : "composer.builtin.profile.applied",
            values: {
              profile: candidate.label,
              count: warnings.length,
              warning: warnings[0] ?? "",
            },
          });
        } catch (error) {
          logger.error("profile switch failed", {
            profileRef: candidate.ref,
            error,
          });
          setNotice({
            tone: "error",
            messageKey: "composer.builtin.profile.failed",
            values: { message: describeError(error) },
          });
        }
      })();
    },
    [profileSelection, runProfileSaveAs, sessionId, setNotice],
  );

  return { runProfile };
}
