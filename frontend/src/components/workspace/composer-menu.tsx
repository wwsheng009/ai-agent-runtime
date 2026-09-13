// P1-4 子片 3：Composer 触发菜单的展示层（`/` 命令、`@` 引用、`+` 按钮三入口同源）。
// 键盘所有权在 textarea（aria-activedescendant），此处只负责渲染、指针高亮与点选。

import { ChevronRightIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  COMPOSER_MENU_LISTBOX_ID,
  composerMenuItemDomId,
} from "@/hooks/workspace/composer/use-composer-menu";
import {
  type ComposerMenuGroup,
  type ComposerMenuItem,
  type ComposerMenuSnapshot,
} from "@/lib/composer-menu";
import { cn } from "@/lib/utils";

type ComposerMenuProps = {
  activeId: string | null;
  onHover: (itemId: string) => void;
  onSelect: (itemId: string) => void;
  snapshot: ComposerMenuSnapshot;
};

export function ComposerMenu({
  activeId,
  onHover,
  onSelect,
  snapshot,
}: ComposerMenuProps) {
  const { t } = useTranslation("workspace");

  function resolveLabel(label: string, labelKey?: string): string {
    // i18n key 来自模型层（字符串），此处收口成类型化 key 的显式断言。
    return labelKey ? (t(labelKey as never) as string) : label;
  }

  return (
    <div
      id={COMPOSER_MENU_LISTBOX_ID}
      role="listbox"
      aria-label={t("composer.menu.label")}
      data-composer-menu
      className="absolute bottom-full left-0 right-0 z-40 mb-2 max-h-[18rem] overflow-y-auto rounded-panel border border-border [background:var(--workspace-composer-bg)] p-1 shadow-[0_16px_40px_rgba(0,0,0,0.4)]"
    >
      {snapshot.empty ? (
        <div
          role="presentation"
          data-composer-menu-empty
          className="px-2 py-2 app-text-11 text-muted-foreground"
        >
          {t("composer.menu.empty")}
        </div>
      ) : null}
      {snapshot.groups.map((group) => (
        <ComposerMenuGroupBlock
          key={group.id}
          activeId={activeId}
          group={group}
          onHover={onHover}
          onSelect={onSelect}
          resolveLabel={resolveLabel}
        />
      ))}
      {!snapshot.empty ? (
        <div className="border-t border-border px-2 pb-1 pt-2 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
          {t("composer.menu.hint")}
        </div>
      ) : null}
    </div>
  );
}

type ComposerMenuGroupBlockProps = {
  activeId: string | null;
  group: ComposerMenuGroup;
  onHover: (itemId: string) => void;
  onSelect: (itemId: string) => void;
  resolveLabel: (label: string, labelKey?: string) => string;
};

function ComposerMenuGroupBlock({
  activeId,
  group,
  onHover,
  onSelect,
  resolveLabel,
}: ComposerMenuGroupBlockProps) {
  const { t } = useTranslation("workspace");
  const label = resolveLabel(group.label, group.labelKey);
  return (
    <div role="group" aria-label={label} data-composer-menu-group={group.id}>
      <div className="px-2 pb-1 pt-2 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </div>
      {group.items.map((item) => (
        <ComposerMenuOption
          key={item.id}
          active={item.id === activeId}
          item={item}
          onHover={onHover}
          onSelect={onSelect}
          resolveLabel={resolveLabel}
          countLabel={item.count ? t("composer.menu.count", { count: item.count }) : null}
        />
      ))}
    </div>
  );
}

type ComposerMenuOptionProps = {
  active: boolean;
  countLabel: string | null;
  item: ComposerMenuItem;
  onHover: (itemId: string) => void;
  onSelect: (itemId: string) => void;
  resolveLabel: (label: string, labelKey?: string) => string;
};

function ComposerMenuOption({
  active,
  countLabel,
  item,
  onHover,
  onSelect,
  resolveLabel,
}: ComposerMenuOptionProps) {
  const { t } = useTranslation("workspace");
  const dataAttribute =
    item.action.kind === "attach"
      ? { "data-composer-attach": "" }
      : item.action.kind === "command"
        ? { "data-composer-command-option": item.action.name }
        : { "data-composer-reference-option": item.action.text };
  return (
    <div
      id={composerMenuItemDomId(item.id)}
      role="option"
      aria-selected={active}
      data-composer-menu-item={item.id}
      {...dataAttribute}
      onMouseDown={(event) => {
        // 保住 textarea 焦点：否则点选会先触发 blur 关闭菜单，点击落空。
        event.preventDefault();
      }}
      onMouseMove={() => onHover(item.id)}
      onClick={() => onSelect(item.id)}
      className={cn(
        "flex cursor-pointer items-center gap-2 rounded-[0.5rem] px-2 py-1.5 app-text-11",
        active
          ? "bg-surface-soft text-foreground"
          : "text-muted-foreground hover:bg-surface-soft",
      )}
    >
      <span className="truncate">{resolveLabel(item.label, item.labelKey)}</span>
      {item.descriptionKey ? (
        <span className="truncate app-text-9 text-muted-foreground/70">
          {t(item.descriptionKey as never) as string}
        </span>
      ) : item.description ? (
        <span className="truncate app-text-9 text-muted-foreground/70">
          {item.description}
        </span>
      ) : null}
      <span className="ml-auto flex shrink-0 items-center gap-1 app-text-9 text-muted-foreground/70">
        {countLabel}
        {item.level === "launcher" ? (
          <ChevronRightIcon size={12} aria-hidden="true" />
        ) : null}
      </span>
    </div>
  );
}
