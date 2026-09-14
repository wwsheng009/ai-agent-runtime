// P2-7 子片 3：`/model` 弹窗（与 composer 常驻座位同一目录、同一处理器）。
//
// 边界：
// - 只渲染宿主持有的目录投影（按 provider 分组），不做任何拉取 / 缓存 / 推断；
// - 选中即调用宿主同一 `onModelChange`（provider 归属由运行时目录解析，弹窗不猜）；
// - 加载中 / 失败 / 目录为空各自如实呈现，**不补占位模型、不伪造默认选中**。

import { useTranslation } from "react-i18next";

import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import type { ComposerModelCatalogGroup } from "@/lib/composer-model-options";
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

export function ComposerModelDialog({
  error,
  groups,
  loading,
  onClose,
  onSelect,
  open,
  selectedModel,
  selectedProvider,
}: ComposerModelDialogProps) {
  const { t } = useTranslation("workspace");
  useDialogLifecycle(open, onClose);

  if (!open) {
    return null;
  }

  const hasModels = groups.some((group) => group.models.length > 0);

  return (
    <DialogOverlay onDismiss={onClose}>
      <DialogPanel
        aria-label={t("composer.modelDialog.title")}
        aria-modal="true"
        className="max-w-[34rem]"
        data-composer-model-dialog
        elevation="lg"
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

        <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
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

          {groups.map((group) => (
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
                    )}
                    data-composer-model-option={model}
                    data-composer-model-option-current={current ? "true" : undefined}
                    key={`${group.provider}:${model}`}
                    onClick={() => onSelect(model)}
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
