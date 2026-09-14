// P2-1A：会话元数据检索弹层（服务端过滤，POST /api/runtime/sessions/search）。
//
// 与侧栏本地标题搜索的区别：这里走服务端 user/tags/state 过滤，是检索结果的
// 唯一来源（不补本地假数据）；后端不可用（404/405/501/503）时如实提示降级，
// 并保留本地标题搜索可用。交互：Esc / 遮罩点击关闭，关闭后焦点回到触发按钮。

import { RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
import { useCallback, useMemo, useState, type FormEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { DEFAULT_SESSION_SEARCH_LIMIT } from "@/api/runtime/session-search";
import { RuntimeApiError } from "@/api/runtime/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import {
  parseSessionSearchTagInput,
  sessionSearchRowTags,
  sessionSearchRowTime,
  sessionSearchRowTitle,
  sessionSearchStateLabelKey,
} from "@/components/workspace/session-search-shared";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { useSessionSearch } from "@/hooks/workspace/use-session-search";
import type { SessionSearchStatus } from "@/hooks/workspace/use-session-search";
import { cn } from "@/lib/utils";
import type {
  RuntimeSessionRecord,
  RuntimeSessionSearchFilters,
  RuntimeSessionSearchResponse,
  RuntimeSessionUserSummary,
} from "@/types/runtime";

/** 后端合法状态枚举（backend/internal/chat/session.go:17-20）。 */
const sessionStateOptions = ["active", "idle", "closed", "archived"] as const;

export type SessionSearchDialogProps = {
  /** 侧栏当前选中的用户；首次打开时作为默认筛选带入。 */
  defaultUserId?: string;
  open: boolean;
  onClose: () => void;
  onSelectSession: (sessionId: string) => void;
  users?: RuntimeSessionUserSummary[];
};

export function SessionSearchDialog({
  defaultUserId,
  onClose,
  onSelectSession,
  open,
  users = [],
}: SessionSearchDialogProps) {
  const { t } = useTranslation("workspace");
  const { error, reset, result, run, status, unavailable } = useSessionSearch();

  const [userId, setUserId] = useState(defaultUserId ?? "");
  const [state, setState] = useState("");
  const [tagsInput, setTagsInput] = useState("");
  const [appliedFilters, setAppliedFilters] =
    useState<RuntimeSessionSearchFilters | null>(null);

  // 关闭（Esc / 遮罩 / 关闭按钮）时中止在途检索并清空结果，避免过期结果回填。
  const handleClose = useCallback(() => {
    reset();
    onClose();
  }, [onClose, reset]);

  useDialogLifecycle(open, handleClose);
  useFocusRestore(open);

  const draftFilters = useMemo<RuntimeSessionSearchFilters>(
    () => ({
      userId: userId.trim() || undefined,
      state: state.trim() || undefined,
      tags: parseSessionSearchTagInput(tagsInput),
      limit: DEFAULT_SESSION_SEARCH_LIMIT,
      offset: 0,
    }),
    [state, tagsInput, userId],
  );

  const submit = useCallback(
    (event?: FormEvent<HTMLFormElement>) => {
      event?.preventDefault();
      setAppliedFilters(draftFilters);
      void run(draftFilters);
    },
    [draftFilters, run],
  );

  const retry = useCallback(() => {
    void run(appliedFilters ?? draftFilters);
  }, [appliedFilters, draftFilters, run]);

  const clearFilters = useCallback(() => {
    setUserId("");
    setState("");
    setTagsInput("");
  }, []);

  if (!open) {
    return null;
  }

  const limit = result?.filters?.limit ?? DEFAULT_SESSION_SEARCH_LIMIT;
  const reachedLimit = (result?.sessions.length ?? 0) >= limit;

  return createPortal(
    <DialogOverlay onDismiss={handleClose}>
      <DialogPanel
        aria-label={t("panels.sessionSearch.ariaLabel")}
        className="max-w-3xl"
        data-testid="session-search-dialog"
        role="dialog"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div className="min-w-0">
            <div className="app-text-11 uppercase tracking-[0.16em] text-accent-secondary">
              {t("panels.sessionSearch.eyebrow")}
            </div>
            <h2 className="mt-1 flex items-center gap-2 text-lg font-semibold tracking-[-0.03em] text-foreground">
              <SearchIcon size={17} className="text-accent-primary" />
              {t("panels.sessionSearch.title")}
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">
              {t("panels.sessionSearch.description")}
            </p>
          </div>
          <Button
            aria-label={t("panels.sessionSearch.close")}
            onClick={handleClose}
            size="icon"
            title={t("panels.sessionSearch.close")}
            variant="ghost"
          >
            <XIcon size={16} />
          </Button>
        </div>

        <form
          className="border-b border-border px-3.5 py-3.5 sm:px-4"
          onSubmit={submit}
        >
          <fieldset className="grid gap-3 sm:grid-cols-3">
            <legend className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.sessionSearch.form.legend")}
            </legend>

            <label className="block text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {t("panels.sessionSearch.form.userId")}
              </span>
              <select
                aria-label={t("panels.sessionSearch.form.userId")}
                className="mt-1 w-full rounded-card border border-border bg-surface-solid px-2 py-1.5 text-sm text-foreground outline-none"
                onChange={(event) => setUserId(event.target.value)}
                value={userId}
              >
                <option value="">
                  {t("panels.sessionSearch.form.userIdAny")}
                </option>
                {users.map((user) => (
                  <option key={user.user_id} value={user.user_id}>
                    {user.display_name?.trim() || user.user_id}
                  </option>
                ))}
              </select>
            </label>

            <label className="block text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {t("panels.sessionSearch.form.state")}
              </span>
              <select
                aria-label={t("panels.sessionSearch.form.state")}
                className="mt-1 w-full rounded-card border border-border bg-surface-solid px-2 py-1.5 text-sm text-foreground outline-none"
                onChange={(event) => setState(event.target.value)}
                value={state}
              >
                <option value="">
                  {t("panels.sessionSearch.form.stateAny")}
                </option>
                {sessionStateOptions.map((option) => (
                  <option key={option} value={option}>
                    {t(sessionSearchStateLabelKey(option))}
                  </option>
                ))}
              </select>
            </label>

            <label className="block text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {t("panels.sessionSearch.form.tags")}
              </span>
              <input
                aria-label={t("panels.sessionSearch.form.tags")}
                className="mt-1 w-full rounded-card border border-border bg-surface-solid px-2 py-1.5 text-sm text-foreground outline-none"
                onChange={(event) => setTagsInput(event.target.value)}
                placeholder={t("panels.sessionSearch.form.tagsPlaceholder")}
                value={tagsInput}
              />
              <span className="mt-1 block leading-5">
                {t("panels.sessionSearch.form.tagsHint")}
              </span>
            </label>
          </fieldset>

          <div className="mt-3 flex items-center gap-2">
            <Button disabled={status === "loading"} type="submit">
              {status === "loading"
                ? t("panels.sessionSearch.form.searching")
                : t("panels.sessionSearch.form.submit")}
            </Button>
            <Button onClick={clearFilters} type="button" variant="ghost">
              {t("panels.sessionSearch.form.reset")}
            </Button>
          </div>
        </form>

        <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3.5 sm:px-4">
          <SearchStatusNotice
            error={error}
            limit={limit}
            onRetry={retry}
            reachedLimit={reachedLimit}
            result={result}
            status={status}
            unavailable={unavailable}
          />

          {result && result.sessions.length > 0 ? (
            <section aria-label={t("panels.sessionSearch.result.title")}>
              <ul className="space-y-1.5">
                {result.sessions.map((session) => (
                  <SessionSearchRow
                    key={session.id}
                    onSelect={onSelectSession}
                    session={session}
                  />
                ))}
              </ul>
            </section>
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}

type SearchStatusNoticeProps = {
  error: unknown;
  limit: number;
  onRetry: () => void;
  reachedLimit: boolean;
  result: RuntimeSessionSearchResponse | null;
  status: SessionSearchStatus;
  unavailable: boolean;
};

function SearchStatusNotice({
  error,
  limit,
  onRetry,
  reachedLimit,
  result,
  status,
  unavailable,
}: SearchStatusNoticeProps) {
  const { t } = useTranslation("workspace");

  if (status === "loading") {
    return (
      <div
        className="flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground"
        data-testid="session-search-loading"
      >
        <RefreshCwIcon size={13} className="animate-spin" />
        {t("panels.sessionSearch.result.loading")}
      </div>
    );
  }

  if (status === "error") {
    const statusCode = error instanceof RuntimeApiError ? error.status : null;
    const message = unavailable
      ? statusCode === null
        ? t("panels.sessionSearch.error.unavailableUnknown")
        : t("panels.sessionSearch.error.unavailable", {
            status: String(statusCode),
          })
      : error instanceof Error
        ? error.message
        : String(error);
    return (
      <div
        className="rounded-card border border-border bg-surface-softer px-3 py-3 text-sm"
        data-testid="session-search-error"
      >
        <div className="font-medium text-analytics-danger">
          {t("panels.sessionSearch.error.title")}
        </div>
        <p className="mt-1 break-words text-xs leading-5 text-muted-foreground">
          {message}
        </p>
        <Button className="mt-2" onClick={onRetry} size="sm" variant="ghost">
          {t("panels.sessionSearch.error.retry")}
        </Button>
      </div>
    );
  }

  if (status === "ready" && result && result.sessions.length === 0) {
    return (
      <div
        className="rounded-card border border-border bg-surface-softer px-3 py-3 text-sm"
        data-testid="session-search-empty"
      >
        <div className="font-medium text-foreground">
          {t("panels.sessionSearch.result.empty")}
        </div>
        <p className="mt-1 text-xs leading-5 text-muted-foreground">
          {t("panels.sessionSearch.result.emptyHint")}
        </p>
      </div>
    );
  }

  if (status === "ready" && result) {
    return (
      <div className="mb-2">
        <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.sessionSearch.result.count", {
            count: result.count,
          })}
        </div>
        {reachedLimit ? (
          <p
            className="mt-1 text-xs leading-5 text-analytics-warning"
            data-testid="session-search-limit-hint"
          >
            {t("panels.sessionSearch.result.limitHint", {
              limit: String(limit),
            })}
          </p>
        ) : null}
      </div>
    );
  }

  return (
    <div className="rounded-card border border-border bg-surface-softer px-3 py-3 text-xs text-muted-foreground">
      {t("panels.sessionSearch.result.idle")}
    </div>
  );
}

type SessionSearchRowProps = {
  onSelect: (sessionId: string) => void;
  session: RuntimeSessionRecord;
};

function SessionSearchRow({ onSelect, session }: SessionSearchRowProps) {
  const { t } = useTranslation("workspace");
  const title = sessionSearchRowTitle(session);
  const tags = sessionSearchRowTags(session);
  const time = sessionSearchRowTime(session);

  return (
    <li>
      <button
        aria-label={t("panels.sessionSearch.result.open", { title })}
        className={cn(
          "w-full rounded-card border border-border bg-surface-softer px-2.5 py-2 text-left transition",
          "hover:border-border-strong hover:bg-surface-soft",
        )}
        data-testid="session-search-row"
        onClick={() => onSelect(session.id)}
        type="button"
      >
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm font-medium text-foreground">
            {title}
          </span>
          <Badge>{t(sessionSearchStateLabelKey(session.state))}</Badge>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span className="font-mono">{session.id}</span>
          {tags.length > 0 ? (
            <span>{t("panels.sessionSearch.result.tags", { tags: tags.join(", ") })}</span>
          ) : null}
          <span>
            {time
              ? t("panels.sessionSearch.result.updatedAt", { time })
              : t("panels.sessionSearch.result.timeUnknown")}
          </span>
        </div>
      </button>
    </li>
  );
}
