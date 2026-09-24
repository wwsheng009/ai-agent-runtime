// Batch 12：`/profile` 弹窗（参考 composer-skill-dialog；无参数提交 `/profile` 时打开）。
//
// 边界：只呈现候选与检索，不拉取数据、不执行切换——点选后把 ref 交还宿主
// （与 `/model` 弹窗同纪律：选择动作走单一处理器，不另建通道）。

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import type { ComposerProfileCandidate } from "@/lib/composer-profile-options";
import { cn } from "@/lib/utils";

export type ComposerProfileDialogProps = {
  candidates: readonly ComposerProfileCandidate[];
  error: string | null;
  loading: boolean;
  onClose: () => void;
  /** 目录拉取失败后的重试（缺省 = 只呈现错误，不给假按钮）。 */
  onRetry?: () => void;
  /** 选择 profile 后交还 ref（宿主执行切换）。 */
  onSelect: (profileRef: string) => void;
  open: boolean;
};

/**
 * 关闭即卸载：检索词与高亮随重开归零。
 */
export function ComposerProfileDialog(props: ComposerProfileDialogProps) {
  useDialogLifecycle(props.open, props.onClose);
  if (!props.open) {
    return null;
  }
  return (
    <ComposerProfileDialogBody
      candidates={props.candidates}
      error={props.error}
      loading={props.loading}
      onClose={props.onClose}
      onRetry={props.onRetry}
      onSelect={props.onSelect}
    />
  );
}

function ComposerProfileDialogBody({
  candidates,
  error,
  loading,
  onClose,
  onRetry,
  onSelect,
}: Omit<ComposerProfileDialogProps, "open">) {
  const { t } = useTranslation("workspace");

  const [search, setSearch] = useState("");
  // null = 未人工导航：高亮在首行
  const [activeOverride, setActiveOverride] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const focusPendingRef = useRef(true);

  // 弹窗只列可解析项（与菜单候选同口径）：解析失败的 profile 不在这里
  // 伪装成可选，宿主回执会给出「存在但不可用 + 原因」。
  const selectable = useMemo(
    () => candidates.filter((candidate) => candidate.valid),
    [candidates],
  );
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    if (needle.length === 0) {
      return selectable;
    }
    return selectable.filter(
      (candidate) =>
        candidate.label.toLowerCase().includes(needle) ||
        candidate.ref.toLowerCase().includes(needle) ||
        candidate.layer.toLowerCase().includes(needle),
    );
  }, [search, selectable]);

  const requestedIndex = activeOverride ?? 0;
  const activeIndex =
    filtered.length === 0
      ? -1
      : Math.max(0, Math.min(requestedIndex, filtered.length - 1));

  useEffect(() => {
    if (focusPendingRef.current && searchRef.current) {
      searchRef.current.focus();
      focusPendingRef.current = false;
    }
  }, [error, loading, filtered]);

  useEffect(() => {
    if (listRef.current && filtered.length > 0) {
      const activeElement = listRef.current.querySelector(
        `[data-profile-index="${activeIndex}"]`,
      );
      // jsdom 未实现 scrollIntoView：测试环境按「无该能力」跳过，不抛错。
      if (activeElement && typeof activeElement.scrollIntoView === "function") {
        activeElement.scrollIntoView({ block: "nearest" });
      }
    }
  }, [activeIndex, filtered.length, search]);

  const handleSelect = useCallback(
    (profileRef: string) => {
      onSelect(profileRef);
      onClose();
    },
    [onSelect, onClose],
  );

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<HTMLInputElement>) => {
      if (filtered.length === 0) {
        return;
      }
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveOverride((prev) => {
          const current = prev ?? 0;
          return Math.min(current + 1, filtered.length - 1);
        });
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveOverride((prev) => {
          const current = prev ?? 0;
          return Math.max(current - 1, 0);
        });
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (activeIndex >= 0) {
          handleSelect(filtered[activeIndex].ref);
        }
      }
    },
    // Escape 不在这里处理：DialogOverlay 的生命周期钩子已在 window 上接管
    // （use-dialog-lifecycle），输入框再处理一次会双触发 onClose。
    [filtered, activeIndex, handleSelect],
  );

  return (
    <DialogOverlay onDismiss={onClose}>
      <DialogPanel className="max-h-[32rem] w-full max-w-md overflow-hidden">
        <div className="flex flex-col gap-3 p-4">
          <h2 className="text-lg font-semibold text-[--text-primary]">
            {t("composer.builtin.profile.dialog.title")}
          </h2>

          <input
            ref={searchRef}
            type="text"
            data-composer-profile-search
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={t("composer.builtin.profile.dialog.searchPlaceholder")}
            className="w-full rounded-lg border border-[--border-subtle] bg-[--surface-level-2] px-3 py-2 text-sm text-[--text-primary] placeholder:text-[--text-tertiary] focus:border-[--accent-primary] focus:outline-none"
          />

          <div
            ref={listRef}
            className="max-h-[20rem] min-h-[8rem] overflow-y-auto rounded-lg border border-[--border-subtle]"
          >
            {loading ? (
              <div
                data-composer-profile-dialog-loading
                className="flex h-32 items-center justify-center text-sm text-[--text-secondary]"
              >
                {t("composer.builtin.profile.dialog.loading")}
              </div>
            ) : error ? (
              <div
                data-composer-profile-dialog-error
                className="flex h-32 flex-col items-center justify-center gap-2 px-4 text-center text-sm text-[--text-error]"
              >
                <span>{error}</span>
                {onRetry ? (
                  <button
                    type="button"
                    onClick={onRetry}
                    className="rounded-md border border-[--border-subtle] px-3 py-1 text-xs text-[--text-primary] hover:bg-[--surface-level-3]"
                  >
                    {t("composer.builtin.profile.dialog.retry")}
                  </button>
                ) : null}
              </div>
            ) : filtered.length === 0 ? (
              <div
                data-composer-profile-dialog-empty-state={
                  search.trim() ? "no-match" : "empty"
                }
                className="flex h-32 items-center justify-center text-sm text-[--text-secondary]"
              >
                {search.trim()
                  ? t("composer.builtin.profile.dialog.noMatch")
                  : t("composer.builtin.profile.dialog.empty")}
              </div>
            ) : (
              <div className="divide-y divide-[--border-subtle]">
                {filtered.map((candidate, index) => (
                  <button
                    key={candidate.ref}
                    data-profile-index={index}
                    data-composer-profile-option={candidate.ref}
                    data-composer-profile-option-active={
                      index === activeIndex ? "true" : undefined
                    }
                    onClick={() => handleSelect(candidate.ref)}
                    onMouseEnter={() => setActiveOverride(index)}
                    className={cn(
                      "w-full px-4 py-2.5 text-left text-sm transition-colors",
                      index === activeIndex
                        ? "bg-[--accent-primary] text-white"
                        : "text-[--text-primary] hover:bg-[--surface-level-3]",
                    )}
                  >
                    <span className="block">{candidate.label}</span>
                    <span className="block text-xs opacity-80">
                      {candidate.isDefault
                        ? `${candidate.layer} · ${t("composer.builtin.profile.dialog.defaultBadge")}`
                        : candidate.layer}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
      </DialogPanel>
    </DialogOverlay>
  );
}
