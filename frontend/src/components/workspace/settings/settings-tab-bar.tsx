/**
 * 设置域通用 Tab 条：runtime config 的 provider 页面与编辑弹窗共用。
 *
 * 渲染与交互收敛到 ui/tab-switcher：选中项实心高亮、roving tabindex 与方向键切换。
 * 只做受控渲染（value + onChange），面板由调用方按 value 自行渲染。
 */

import type { ReactNode } from "react";

import { TabSwitcher } from "@/components/ui/tab-switcher";

export type SettingsTabItem<T extends string> = {
  id: T;
  label: string;
  badge?: ReactNode;
  disabled?: boolean;
};

export function SettingsTabBar<T extends string>({
  ariaLabel,
  className,
  items,
  onChange,
  value,
}: {
  ariaLabel: string;
  className?: string;
  items: ReadonlyArray<SettingsTabItem<T>>;
  onChange: (id: T) => void;
  value: T;
}) {
  return (
    <TabSwitcher
      ariaLabel={ariaLabel}
      className={className}
      items={items}
      onChange={onChange}
      value={value}
      variant="segmented"
    />
  );
}
