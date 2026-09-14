// P2-7 子片 3：`/model` 弹窗（与 composer 常驻座位同一目录、同一处理器）。
// P2-7 子片 4：候选面按目标项目 `PopupSelectView` 语义补齐——弹层自持焦点（检索框）、
//   本地检索过滤、方向键虚拟高亮（Enter 应用、←/→ 交给检索框原生光标）、高亮行滚动入视口。
//
// 边界：
// - 只渲染宿主持有的目录投影（按 provider 分组），不做任何拉取 / 缓存 / 推断；
// - 选中即调用宿主同一 `onModelChange`（provider 归属由运行时目录解析，弹窗不猜）；
// - 加载中 / 失败 / 目录为空 / 检索无匹配各自如实呈现，**不补占位模型、不伪造默认选中**。

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
import {
  filterComposerModelGroups,
  type ComposerModelCatalogGroup,
} from "@/lib/composer-model-options";
import { cn } from "@/lib/utils";

/** 分组视图与 `lib/composer-model-options` 同源（此处仅保留组件侧的名字）。 */
export type ComposerModelDialogGroup = ComposerModelCatalogGroup;

export type ComposerModelDialogProps = {
  error: string | null;
  groups: readonly ComposerModelDialogGroup[];
  loading: boolean;
  onClose: () => void;
  /** 应用模型：与常驻座位同一处理器。 */
  onSelect: (model: string) => void;
  open: boolean;
  selectedModel: string;
  selectedProvider: string;
};

/** 扁平行：高亮 / 键盘应用都以 (provider, model) 为身份，不合并同名模型。 */
type ComposerModelRow = { provider: string; model: string };

function rowKey(provider: string, model: string): string {
  return `${provider}\u0000${model}`;
}

/**
 * 关闭即卸载：候选面的检索词与高亮随重开归零（不靠 effect 里同步 setState 复位）。
 * 外壳只持有对话框生命周期，其余状态都落在随 `open` 挂载的 `ComposerModelDialogBody`。
 */
export function ComposerModelDialog(props: ComposerModelDialogProps) {
  useDialogLifecycle(props.open, props.onClose);
  if (!props.open) {
    return null;
  }
  return (
    <ComposerModelDialogBody
      error={props.error}
      groups={props.groups}
      loading={props.loading}
      onClose={props.onClose}
      onSelect={props.onSelect}
      selectedModel={props.selectedModel}
      selectedProvider={props.selectedProvider}
    />
  );
}

function ComposerModelDialogBody({
  error,
  groups,
  loading,
  onClose,
  onSelect,
  selectedModel,
  selectedProvider,
}: Omit<ComposerModelDialogProps, "open">) {
  const { t } = useTranslation("workspace");

  const [search, setSearch] = useState("");
  // `null` = 尚未人工导航：高亮跟住当前座位（目录晚到时自动落到当前座位 / 首行）。
  const [activeOverride, setActiveOverride] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const focusPendingRef = useRef(true);

  const filteredGroups = useMemo(
    () => filterComposerModelGroups(groups, search),
    [groups, search],
  );
  const rows = useMemo<ComposerModelRow[]>(
    () =>
      filteredGroups.flatMap((group) =>
        group.models.map((model) => ({ provider: group.provider, model })),
      ),
    [filteredGroups],
  );
  const rowIndex = useMemo(
    () => new Map(rows.map((row, index) => [rowKey(row.provider, row.model), index])),
    [rows],
  );
  const currentIndex = useMemo(
    () =>
      rows.findIndex(
        (row) => row.model === selectedModel && row.provider === selectedProvider,
      ),
    [rows, selectedModel, selectedProvider],
  );
  // 当前座位解析不到时预高亮首行（与打开时一致），解析得到即跟住座位。
  const requestedIndex = activeOverride ?? (currentIndex >= 0 ? currentIndex : 0);
  const activeIndex =
    rows.length === 0 ? -1 : Math.max(0, Math.min(requestedIndex, rows.length - 1));
  const activeRow = activeIndex >= 0 ? rows[activeIndex] : undefined;

  // 与目标项目 PopupSelectView 一致：弹层持有焦点，打开即可直接打字检索。
  // 目录晚到时检索框更晚挂载：把「待聚焦」保持到输入框真正出现，不把焦点丢在遮罩上。
  useEffect(() => {
    if (focusPendingRef.current && searchRef.current) {
      focusPendingRef.current = false;
      searchRef.current.focus();
    }
  }, [error, loading, rows]);

  // 虚拟高亮不触发浏览器默认滚动，这里把高亮行滚入视口。
  useEffect(() => {
    const node = listRef.current?.querySelector(
      '[data-composer-model-option-active="true"]',
    );
    if (node && typeof node.scrollIntoView === "function") {
      node.scrollIntoView({ block: "nearest" });
    }
  }, [activeIndex, search]);

  const handleKeyDown = useCallback(
    (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (rows.length === 0) {
          return;
        }
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        const base = activeIndex >= 0 ? activeIndex : 0;
        setActiveOverride(Math.max(0, Math.min(base + step, rows.length - 1)));
        return;
      }
      if (event.key === "Enter") {
        if (!activeRow) {
          return;
        }
        // 阻止 Enter 触发行按钮的默认激活，避免同一次按键应用两次。
        event.preventDefault();
        onSelect(activeRow.model);
      }
    },
    [activeIndex, activeRow, onSelect, rows.length],
  );

  const hasModels = groups.some((group) => group.models.length > 0);

  return (
    <DialogOverlay onDismiss={onClose}>
      <DialogPanel
        aria-label={t("composer.modelDialog.title")}
        aria-modal="true"
        className="max-w-[34rem]"
        data-composer-model-dialog
        elevation="lg"
        onKeyDown={handleKeyDown}
        role="dialog"
      >
        <header className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <h2 className="app-text-13 font-medium text-foreground">
              {t("composer.modelDialog.title")}
            </h2>
            <p className="mt-0.5 app-text-11 text-muted-foreground">
              {t("composer.modelDialog.current", {
                model: selectedModel || t("composer.runtimeDefaultModel"),
                provider: selectedProvider || t("composer.modelDialog.noProvider"),
              })}
            </p>
          </div>
          <button
            aria-label={t("composer.modelDialog.close")}
            className="ml-auto rounded-md px-2 py-1 app-text-11 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
            data-composer-model-dialog-close
            onClick={onClose}
            type="button"
          >
            {t("composer.modelDialog.close")}
          </button>
        </header>

        {hasModels ? (
          <div className="border-b border-border px-3 py-2">
            <input
              aria-label={t("composer.modelDialog.search.aria")}
              className="w-full rounded-md border border-border bg-transparent px-2 py-1.5 app-text-11 text-foreground outline-none placeholder:text-muted-foreground"
              data-composer-model-search
              onChange={(event) => {
                setSearch(event.currentTarget.value);
                setActiveOverride(null);
              }}
              placeholder={t("composer.modelDialog.search.placeholder")}
              ref={searchRef}
              type="text"
              value={search}
            />
          </div>
        ) : null}

        <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2" ref={listRef}>
          {loading && !hasModels ? (
            <div
              className="px-2 py-3 app-text-11 text-muted-foreground"
              data-composer-model-dialog-loading
            >
              {t("composer.modelDialog.loading")}
            </div>
          ) : null}

          {error ? (
            <div
              className="px-2 py-3 app-text-11 text-[#d8a66d]"
              data-composer-model-dialog-error
              role="alert"
            >
              {t("composer.modelDialog.error", { message: error })}
            </div>
          ) : null}

          {!loading && !error && !hasModels ? (
            <div
              className="px-2 py-3 app-text-11 text-muted-foreground"
              data-composer-model-dialog-empty
            >
              {t("composer.modelDialog.empty")}
            </div>
          ) : null}

          {!loading && !error && hasModels && rows.length === 0 ? (
            <div
              className="px-2 py-3 app-text-11 text-muted-foreground"
              data-composer-model-dialog-no-match
            >
              {t("composer.modelDialog.noMatch")}
            </div>
          ) : null}

          {filteredGroups.map((group) => (
            <section
              className="mb-1"
              data-composer-model-provider={group.provider}
              key={group.provider}
            >
              <div className="flex items-center gap-2 px-2 pb-1 pt-2 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
                <span className="truncate">{group.provider}</span>
                <span className="ml-auto shrink-0 tabular-nums">
                  {t("composer.modelDialog.groupCount", {
                    count: group.models.length,
                  })}
                </span>
              </div>
              {group.models.map((model) => {
                const index = rowIndex.get(rowKey(group.provider, model));
                const highlighted =
                  activeRow?.model === model && activeRow?.provider === group.provider;
                const current =
                  model === selectedModel && group.provider === selectedProvider;
                return (
                  <button
                    aria-current={current ? "true" : undefined}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-[0.5rem] px-2 py-1.5 text-left app-text-11 transition",
                      current
                        ? "bg-surface-soft text-foreground"
                        : "text-muted-foreground hover:bg-surface-soft",
                      highlighted && !current ? "bg-surface-soft/60 text-foreground" : null,
                    )}
                    data-composer-model-option={model}
                    data-composer-model-option-active={highlighted ? "true" : undefined}
                    data-composer-model-option-current={current ? "true" : undefined}
                    key={`${group.provider}:${model}`}
                    onClick={() => onSelect(model)}
                    onMouseEnter={() => {
                      if (index !== undefined) {
                        setActiveOverride(index);
                      }
                    }}
                    type="button"
                  >
                    <span className="truncate">{model}</span>
                    {current ? (
                      <span className="ml-auto shrink-0 app-text-9 text-muted-foreground/70">
                        {t("composer.modelDialog.currentBadge")}
                      </span>
                    ) : null}
                  </button>
                );
              })}
            </section>
          ))}
        </div>
      </DialogPanel>
    </DialogOverlay>
  );
}
