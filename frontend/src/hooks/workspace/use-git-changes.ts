// 工作区右侧栏「Git 变更面」数据状态机（规划文档 §4.6 / 阶段 P3-4、P3-5、P3-7、P4-1）。
//
// 后端契约（api/runtime/git.ts）：
//   /git/status  ?scope&path            → repo/clean + staged|unstaged|untracked|conflicts + warnings
//   /git/diff    ?scope&path&file&target&context&whitespace → 结构化 hunks + raw + parse_error + truncated
//   /git/commits ?scope&path&limit&cursor                    → commits + next_cursor + has_more
//   /git/stage   POST {scope,path,action,files}              → action/files/status(同 status 端点)/generated_at
//
// 归一化纪律（竞态口径对齐 hooks/workspace/use-file-preview.ts）：
//   * 每类请求各自持有「递增序号 + AbortController」：切换文件 / target / 空白开关 / 刷新都中止在途请求，
//     响应写回前必须比对序号 —— 旧结果不得覆盖新选择；
//   * 主动取消（AbortError）不落错误态；真实失败保留上一次 data，只置错误态（不把陈旧数据当新结论）；
//   * 空白忽略是**服务端参数**：开关变化必然重新请求 `whitespace=ignore_all`，前端不做假过滤；
//   * `parse_error` / `truncated` / `is_binary` 原样透传，本层不翻译成空 diff 或空列表。
//
// 写操作纪律（P4-1，仅 stage/unstage；**不自动提交**）：
//   * 乐观更新只动本地分组顺序（按 porcelain 语义把文件在组间搬移），失败用请求前快照回滚；
//   * 成功以**服务端返回的 status** 覆盖本地（服务端是唯一结论）；缺失时保留本地快照，不伪造空状态；
//   * 单飞：同一时刻只允许一个写请求（在途时 UI 禁用按钮，重复调用直接忽略），避免「同一文件两次 add」；
//   * 超时（默认 20s）与主动取消区分：取消静默，超时按失败处理（回滚 + 文案）；
//   * 写操作改变索引 → 当前查看文件的 diff 结论过期：重新请求 diff（diff 目标保持用户选择，不自动改）。
//
// 降级判据：
//   * 仓库缺失 / git 不可用（400/404/503，isGitRepoMissing）→ `unavailable=true`，UI 给可解释空态；
//   * 无可用 scope（无会话且无工作目录）→ 不发请求，暴露 `scopeMissing=true`；
//   * 刷新后选中文件可能已离开变更列表 → `selectedStillChanged=false` 如实标注，不静默清空正在看的 diff。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  fetchGitCommits,
  fetchGitDiff,
  fetchGitStatus,
  isGitRepoMissing,
  submitGitStage,
} from "@/api/runtime/git";
import { fetchFsRoots, pickDefaultRoot } from "@/api/runtime/fs-roots";
import { buildFallbackRoot } from "@/lib/file-browser/path-utils";
import {
  DEFAULT_DIFF_ROW_LIMIT,
  DIFF_CONTEXT_DEFAULT,
  DIFF_CONTEXT_EXPAND_STEP,
  MAX_DIFF_CONTEXT,
  nextDiffContext,
} from "@/lib/git/diff-view-model";
import { applyOptimisticStage, flattenGitChanges } from "@/lib/git/change-model";
import type { GitChangeEntry } from "@/lib/git/change-model";
import { createRequestChannel, idleSnapshot, isAbortError } from "@/lib/git/request-channel";
import type { GitSnapshot } from "@/lib/git/request-channel";
import type {
  GitCommitsResult,
  GitDiffResult,
  GitDiffTarget,
  GitStageAction,
  GitStatusResult,
} from "@/types/runtime/git-browse";

// 类型出口保持稳定：组件一直从本模块导入 GitSnapshot / GitChangeEntry，
// 实现已下沉到 lib/git（request-channel / change-model），这里只做再导出。
export type { GitChangeEntry } from "@/lib/git/change-model";
export type { GitLoadStatus, GitSnapshot } from "@/lib/git/request-channel";
export { flattenGitChanges } from "@/lib/git/change-model";

export type GitWhitespaceMode = "show" | "ignore_all";
export type UseGitChangesOptions = { sessionId: string; workspacePath?: string };

export type UseGitChangesResult = {
  scope: string;
  status: GitSnapshot<GitStatusResult>;
  /** 扁平顺序（冲突 → 已暂存 → 未暂存 → 未跟踪），供键盘 ↑/↓ 与列表渲染共用。 */
  entries: GitChangeEntry[];
  reloadStatus: () => void;
  selectedPath: string | null;
  selectFile: (path: string) => void;
  /** 选中文件是否仍在变更列表中（false = 可能已提交 / 还原）。 */
  selectedStillChanged: boolean;
  diff: GitSnapshot<GitDiffResult>;
  target: GitDiffTarget;
  setTarget: (target: GitDiffTarget) => void;
  whitespace: GitWhitespaceMode;
  setWhitespace: (mode: GitWhitespaceMode) => void;
  context: number;
  expandContext: (step?: number) => void;
  canExpandContext: boolean;
  rowLimit: number;
  showMoreRows: () => void;
  retryDiff: () => void;
  commits: GitSnapshot<GitCommitsResult>;
  commitsLoadingMore: boolean;
  loadMoreCommits: () => void;
  reloadCommits: () => void;
  /** 写操作（P4-1）：在途时 UI 必须禁用同组按钮，避免重复提交同一文件。 */
  stagePending: boolean;
  /** 在途文件（作用域相对路径）；非在途时为空数组。 */
  stagingPaths: string[];
  /** 写操作失败原因（超时/越界/仓库不可用等）；成功后清空。 */
  stageError: unknown;
  clearStageError: () => void;
  /** 暂存 / 取消暂存：files 为空、无 scope、已有在途写请求时直接忽略。 */
  stageFiles: (files: string[], action: GitStageAction) => void;
};

const COMMITS_PAGE_LIMIT = 30;
/** 写操作超时：`git add` 之后还要重取 status，给足余量但不无限等待。 */
const STAGE_TIMEOUT_MS = 20_000;

export function useGitChanges({
  sessionId,
  workspacePath,
}: UseGitChangesOptions): UseGitChangesResult {
  const [scope, setScope] = useState("");
  const [status, setStatus] = useState<GitSnapshot<GitStatusResult>>(idleSnapshot);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [target, setTarget] = useState<GitDiffTarget>("working");
  const [whitespace, setWhitespace] = useState<GitWhitespaceMode>("show");
  const [context, setContext] = useState(DIFF_CONTEXT_DEFAULT);
  const [diff, setDiff] = useState<GitSnapshot<GitDiffResult>>(idleSnapshot);
  const [rowLimit, setRowLimit] = useState(DEFAULT_DIFF_ROW_LIMIT);
  const [commits, setCommits] = useState<GitSnapshot<GitCommitsResult>>(idleSnapshot);
  const [commitsLoadingMore, setCommitsLoadingMore] = useState(false);
  const [statusToken, setStatusToken] = useState(0);
  const [diffToken, setDiffToken] = useState(0);
  const [commitsToken, setCommitsToken] = useState(0);
  const [stagePending, setStagePending] = useState(false);
  const [stagingPaths, setStagingPaths] = useState<string[]>([]);
  const [stageError, setStageError] = useState<unknown>(null);

  const statusChannel = useRef(createRequestChannel());
  const diffChannel = useRef(createRequestChannel());
  const commitsChannel = useRef(createRequestChannel());
  /** 写操作单飞通道：seq 用于丢弃过期响应，controller 非空即「在途」。 */
  const stageWrite = useRef<{ seq: number; controller: AbortController | null }>({
    seq: 0,
    controller: null,
  });
  /** 写操作回滚基线（请求发出前的服务端快照）。 */
  const statusRef = useRef(status);
  /** 只有「文件 / target / 空白开关」变化才重置续看行数；扩大 context 不打断已展开的预算。 */
  const rowLimitKey = useRef("");

  useEffect(() => {
    statusRef.current = status;
  }, [status]);

  useEffect(() => {
    const status = statusChannel.current;
    const diff = diffChannel.current;
    const commits = commitsChannel.current;
    const stage = stageWrite.current;
    return () => {
      status.abort();
      diff.abort();
      commits.abort();
      stage.controller?.abort();
      stage.controller = null;
    };
  }, []);

  // scope 解析：**带上 sessionId**，让后端把「会话当前目录」也作为候选根返回；
  // 默认取「会话目录（kind=session）」根，其次服务端顺序首根；端点不可用或失败时用会话/工作目录兜底根。
  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    const fallback = () => buildFallbackRoot(sessionId, workspacePath).scope;
    void (async () => {
      try {
        const result = await fetchFsRoots({ signal: controller.signal, sessionId });
        if (active) {
          setScope(pickDefaultRoot(result.roots)?.scope ?? fallback());
        }
      } catch (caught) {
        if (!active || isAbortError(caught)) {
          return;
        }
        // 端点不可用（isFsRootsUnavailable）与普通失败统一退到兜底根：
        // 让后续 git 请求自己去如实报错，而不是让整面空白。
        setScope(fallback());
      }
    })();
    return () => {
      active = false;
      controller.abort();
    };
  }, [sessionId, workspacePath]);

  useEffect(() => {
    if (!scope) {
      setStatus(idleSnapshot());
      return;
    }
    setStatus((previous) => ({ ...previous, status: "loading", error: null }));
    statusChannel.current.run(
      (signal) => fetchGitStatus({ scope, path: workspacePath ?? "" }, { signal }),
      (result) => setStatus({ status: "ready", data: result, error: null, unavailable: false }),
      (caught) =>
        setStatus((previous) => ({
          ...previous,
          status: "error",
          error: caught,
          unavailable: isGitRepoMissing(caught),
        })),
    );
  }, [scope, workspacePath, statusToken]);

  const entries = useMemo(() => flattenGitChanges(status.data), [status.data]);

  // 首次拿到列表时选中第一项（键盘 ↑/↓ 有起点）；不做自动重选，避免覆盖用户选择。
  useEffect(() => {
    if (selectedPath === null && entries.length > 0) {
      setSelectedPath(entries[0].path);
    }
  }, [entries, selectedPath]);

  useEffect(() => {
    if (!scope || !selectedPath) {
      diffChannel.current.invalidate();
      setDiff(idleSnapshot());
      return;
    }
    setDiff((previous) => ({ ...previous, status: "loading", error: null }));
    diffChannel.current.run(
      (signal) =>
        fetchGitDiff(
          { scope, path: workspacePath ?? "", file: selectedPath, target, context, whitespace },
          { signal },
        ),
      (result) => {
        setDiff({ status: "ready", data: result, error: null, unavailable: false });
        const key = `${selectedPath}|${target}|${whitespace}`;
        if (rowLimitKey.current !== key) {
          rowLimitKey.current = key;
          setRowLimit(DEFAULT_DIFF_ROW_LIMIT);
        }
      },
      (caught) =>
        setDiff((previous) => ({
          ...previous,
          status: "error",
          error: caught,
          unavailable: isGitRepoMissing(caught),
        })),
    );
  }, [scope, workspacePath, selectedPath, target, context, whitespace, diffToken]);

  useEffect(() => {
    if (!scope) {
      setCommits(idleSnapshot());
      return;
    }
    setCommits((previous) => ({ ...previous, status: "loading", error: null }));
    commitsChannel.current.run(
      (signal) =>
        fetchGitCommits(
          { scope, path: workspacePath ?? "", limit: COMMITS_PAGE_LIMIT },
          { signal },
        ),
      (result) => setCommits({ status: "ready", data: result, error: null, unavailable: false }),
      (caught) =>
        setCommits((previous) => ({
          ...previous,
          status: "error",
          error: caught,
          unavailable: isGitRepoMissing(caught),
        })),
    );
  }, [scope, workspacePath, commitsToken]);

  // 翻页复用同一通道的序号纪律：翻页失败保留已加载页，只增量写回新页（按 sha 去重）。
  const loadMoreCommits = useCallback(() => {
    const current = commits.data;
    if (!scope || !current?.hasMore || !current.nextCursor || commitsLoadingMore) {
      return;
    }
    const channel = commitsChannel.current;
    const cursor = current.nextCursor;
    setCommitsLoadingMore(true);
    channel.run(
      (signal) =>
        fetchGitCommits(
          { scope, path: workspacePath ?? "", limit: COMMITS_PAGE_LIMIT, cursor },
          { signal },
        ),
      (page) => {
        setCommits((previous) => {
          const seen = new Set((previous.data?.commits ?? []).map((item) => item.sha));
          return {
            status: "ready",
            data: {
              commits: [
                ...(previous.data?.commits ?? []),
                ...page.commits.filter((item) => !seen.has(item.sha)),
              ],
              nextCursor: page.nextCursor,
              hasMore: page.hasMore,
            },
            error: null,
            unavailable: false,
          };
        });
        setCommitsLoadingMore(false);
      },
      (caught) => {
        setCommits((previous) => ({ ...previous, error: caught }));
        setCommitsLoadingMore(false);
      },
    );
  }, [commits.data, commitsLoadingMore, scope, workspacePath]);

  /**
   * 暂存 / 取消暂存（P4-1）。流程固定为：本地乐观搬移 → 服务端写 → 成功用服务端 status 覆盖 /
   * 失败回滚基线并把错误交给 UI。**不自动提交**，也不在成功后自动改变 diff 目标。
   */
  const stageFiles = useCallback(
    (files: string[], action: GitStageAction) => {
      const wanted = files.map((file) => file.trim()).filter(Boolean);
      const channel = stageWrite.current;
      // 单飞：在途写请求未结束前忽略新请求（UI 同步禁用按钮，这里是最后一道防线）。
      if (!scope || wanted.length === 0 || channel.controller) {
        return;
      }

      const baseline = statusRef.current.data;
      if (baseline) {
        setStatus((previous) =>
          previous.data
            ? { ...previous, data: applyOptimisticStage(previous.data, wanted, action) }
            : previous,
        );
      }
      setStageError(null);
      setStagePending(true);
      setStagingPaths(wanted);

      channel.seq += 1;
      const current = channel.seq;
      const controller = new AbortController();
      channel.controller = controller;
      let timedOut = false;
      const timer = setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, STAGE_TIMEOUT_MS);

      void (async () => {
        try {
          const result = await submitGitStage(
            { scope, path: workspacePath ?? "", action, files: wanted },
            { signal: controller.signal },
          );
          if (channel.seq !== current) {
            return;
          }
          channel.controller = null;
          setStagePending(false);
          setStagingPaths([]);
          // 服务端 status 是唯一结论；缺失（协议异常）时保留乐观结果，不退化成「空仓库」。
          setStatus((previous) => ({
            status: "ready",
            data: result.status ?? previous.data,
            error: null,
            unavailable: false,
          }));
          // 索引已变 → 正在查看的被操作文件 diff 结论过期，重新拉取（target 保持用户选择）。
          if (selectedPath !== null && wanted.includes(selectedPath)) {
            setDiffToken((token) => token + 1);
          }
        } catch (caught) {
          if (channel.seq !== current) {
            return;
          }
          channel.controller = null;
          setStagePending(false);
          setStagingPaths([]);
          if (isAbortError(caught) && !timedOut) {
            // 主动取消（卸载 / 新一轮）静默；基线此时已无意义（组件可能已卸载）。
            return;
          }
          if (baseline) {
            setStatus((previous) => ({ ...previous, data: baseline }));
          }
          setStageError(
            timedOut ? new Error(`git stage timed out after ${STAGE_TIMEOUT_MS}ms`) : caught,
          );
        } finally {
          clearTimeout(timer);
        }
      })();
    },
    [scope, workspacePath, selectedPath],
  );

  return {
    scope,
    status,
    entries,
    reloadStatus: () => setStatusToken((token) => token + 1),
    selectedPath,
    selectFile: setSelectedPath,
    selectedStillChanged:
      selectedPath !== null && entries.some((entry) => entry.path === selectedPath),
    diff,
    target,
    setTarget,
    whitespace,
    setWhitespace,
    context,
    expandContext: (step = DIFF_CONTEXT_EXPAND_STEP) =>
      setContext((previous) => nextDiffContext(previous, step, MAX_DIFF_CONTEXT) ?? previous),
    canExpandContext: context < MAX_DIFF_CONTEXT,
    rowLimit,
    showMoreRows: () => setRowLimit((previous) => previous + DEFAULT_DIFF_ROW_LIMIT),
    retryDiff: () => setDiffToken((token) => token + 1),
    commits,
    commitsLoadingMore,
    loadMoreCommits,
    reloadCommits: () => setCommitsToken((token) => token + 1),
    stagePending,
    stagingPaths,
    stageError,
    clearStageError: () => setStageError(null),
    stageFiles,
  };
}
