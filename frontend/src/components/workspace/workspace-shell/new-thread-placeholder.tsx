// 新线程空态：品牌标 + 引导标题 + 建议卡网格。
//
// 为什么单独成文件：main-section.tsx 已贴着 P0-2 的 500 行上限，而 composer
// 新增的上下文控件必须从那里注入；按既有的「机械拆分」口径把这段纯展示块搬出来。
// t 由宿主注入（与 view-tab-bar / right-rail-section 同一约定），不在这里新建 i18n 订阅。

import { type TFunction } from "i18next";
import { ArrowUpRightIcon, BotIcon, type LucideIcon } from "lucide-react";

export type NewThreadSuggestion = {
  key: string;
  icon: LucideIcon;
  title: string;
  description: string;
  prompt: string;
};

export type NewThreadPlaceholderProps = {
  onDraftChange: (value: string) => void;
  suggestions: readonly NewThreadSuggestion[];
  t: TFunction<"workspace">;
};

export function NewThreadPlaceholder({
  onDraftChange,
  suggestions,
  t,
}: NewThreadPlaceholderProps) {
  return (
    <div className="mx-auto flex w-full max-w-[46rem] flex-1 flex-col justify-center pb-4">
      <div className="text-center">
        <div className="mx-auto grid size-11 place-items-center rounded-[1rem] border border-accent-primary-border bg-accent-primary-soft text-accent-primary shadow-[0_8px_24px_var(--accent-primary-shadow)]">
          <BotIcon size={20} />
        </div>
        <h1 className="mt-3 text-[1.45rem] font-semibold tracking-[-0.03em] text-foreground sm:text-[1.7rem]">
          {t("shell.newChatTitle")}
        </h1>
      </div>
      <div className="mt-5 grid grid-cols-2 gap-2 sm:gap-3">
        {suggestions.map((suggestion) => {
          const SuggestionIcon = suggestion.icon;

          return (
            <button
              key={suggestion.key}
              type="button"
              onClick={() => onDraftChange(suggestion.prompt)}
              className="group flex min-h-[5.5rem] items-start gap-3 rounded-panel border border-border bg-surface-softer px-3 py-3 text-left transition hover:border-border-strong hover:bg-surface-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:px-3.5"
            >
              <span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
                <SuggestionIcon size={15} />
              </span>
              <span className="min-w-0 flex-1">
                <span className="flex items-center justify-between gap-2 text-sm font-semibold text-foreground">
                  {suggestion.title}
                  <ArrowUpRightIcon
                    size={13}
                    className="shrink-0 text-muted-foreground transition group-hover:-translate-y-0.5 group-hover:translate-x-0.5 group-hover:text-foreground"
                  />
                </span>
                <span className="mt-1 block text-xs leading-5 text-muted-foreground">
                  {suggestion.description}
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
