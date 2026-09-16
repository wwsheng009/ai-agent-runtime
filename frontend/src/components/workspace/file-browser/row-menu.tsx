// 目录树行的右键菜单（P4-4）：无状态展示层，动作由调用方决定。
//
// 纪律：
//   * 菜单不自己决定「能不能下载」——禁用项由调用方以 `disabled` 显式给出理由（title 透传）；
//   * 关闭路径三条都要有：Escape、点击菜单外、滚动/窗口尺寸变化（固定定位菜单不跟随滚动，留着就是错位残影）；
//   * 键盘：打开即聚焦第一项，↑/↓ 在项间移动，Tab 关闭（避免把焦点丢到页面其它地方后菜单还在）。
import { useEffect, useRef, type KeyboardEvent } from "react";

export type FileRowMenuItem = {
  id: string;
  label: string;
  onSelect: () => void;
  disabled?: boolean;
  /** 禁用原因 / 补充说明（同时作为按钮 title）。 */
  hint?: string;
};

export function FileRowMenu({
  items,
  x,
  y,
  onClose,
}: {
  items: readonly FileRowMenuItem[];
  x: number;
  y: number;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const handlePointerDown = (event: MouseEvent) => {
      if (!ref.current?.contains(event.target as Node)) {
        onClose();
      }
    };
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    document.addEventListener("mousedown", handlePointerDown, true);
    document.addEventListener("keydown", handleKeyDown, true);
    window.addEventListener("resize", onClose);
    window.addEventListener("scroll", onClose, true);
    ref.current?.querySelector("button")?.focus();
    return () => {
      document.removeEventListener("mousedown", handlePointerDown, true);
      document.removeEventListener("keydown", handleKeyDown, true);
      window.removeEventListener("resize", onClose);
      window.removeEventListener("scroll", onClose, true);
    };
  }, [onClose]);

  const move = (event: KeyboardEvent<HTMLDivElement>, delta: number) => {
    const buttons = [...(ref.current?.querySelectorAll("button:not([disabled])") ?? [])];
    if (buttons.length === 0) {
      return;
    }
    event.preventDefault();
    const index = buttons.findIndex((button) => button === document.activeElement);
    const next = (index + delta + buttons.length) % buttons.length;
    (buttons[index < 0 ? 0 : next] as HTMLButtonElement | undefined)?.focus();
  };

  return (
    <div
      className="fixed z-50 grid min-w-40 gap-0.5 rounded-card border border-border/70 bg-surface p-1 shadow-lg"
      data-testid="file-row-menu"
      onContextMenu={(event) => event.preventDefault()}
      onKeyDown={(event) => {
        if (event.key === "ArrowDown") {
          move(event, 1);
        } else if (event.key === "ArrowUp") {
          move(event, -1);
        } else if (event.key === "Tab") {
          onClose();
        }
      }}
      ref={ref}
      role="menu"
      style={{ left: x, top: y }}
    >
      {items.map((item) => (
        <button
          className="rounded-chip px-2 py-1 text-left app-text-11 hover:bg-white/6 disabled:opacity-40 disabled:hover:bg-transparent"
          disabled={item.disabled}
          key={item.id}
          onClick={() => {
            item.onSelect();
            onClose();
          }}
          role="menuitem"
          title={item.hint}
          type="button"
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}
