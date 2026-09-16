// 文件面板的拖放上传投放区（P4-3）。
//
// 纪律（与规划文档 §4.4 一致）：
//   * 只认真实文件拖放：`dataTransfer.types` 不含 `Files` 时一律不提示、不接管（避免拖选文本时闪出遮罩）；
//   * 不预判目标是否可写：服务端 409/403 由传输托盘按既有冲突策略呈现，这里不静默覆盖；
//   * 拖拽计数器抵消子元素 dragenter/dragleave 抖动（经典 enter/leave 成对计数），
//     并在 drop / dragleave 归零 / 组件卸载时清理，避免遮罩卡住。
import { useEffect, useRef, useState, type DragEvent, type ReactNode } from "react";

import { cn } from "@/lib/utils";

function hasFiles(event: DragEvent<HTMLElement>): boolean {
  const types = event.dataTransfer?.types;
  if (!types) {
    return false;
  }
  return Array.from(types).includes("Files");
}

export function FileDropTarget({
  children,
  hint,
  onDropFiles,
  className,
}: {
  children: ReactNode;
  /** 遮罩文案（由调用方带入目标目录，组件本身不猜路径）。 */
  hint: string;
  onDropFiles: (files: FileList) => void;
  className?: string;
}) {
  const [active, setActive] = useState(false);
  const depth = useRef(0);

  useEffect(() => {
    return () => {
      depth.current = 0;
    };
  }, []);

  return (
    <div
      // 必须是 **flex 列容器**：子面在内部用 `flex-1` + `minmax(0,…)` 网格建立高度链，
      // 若这里只是块级 div，内部网格高度退化为内容高度 → 树不产生滚动条、预览区被裁掉。
      className={cn("flex min-h-0 flex-col", className)}
      data-testid="file-drop-target"
      onDragEnter={(event) => {
        if (!hasFiles(event)) {
          return;
        }
        event.preventDefault();
        depth.current += 1;
        setActive(true);
      }}
      onDragLeave={(event) => {
        if (!hasFiles(event)) {
          return;
        }
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) {
          setActive(false);
        }
      }}
      onDragOver={(event) => {
        if (!hasFiles(event)) {
          return;
        }
        // 不 preventDefault 就不会触发 drop（浏览器默认把文件当作导航）。
        event.preventDefault();
        if (event.dataTransfer) {
          event.dataTransfer.dropEffect = "copy";
        }
      }}
      onDrop={(event) => {
        if (!hasFiles(event)) {
          return;
        }
        event.preventDefault();
        depth.current = 0;
        setActive(false);
        const files = event.dataTransfer?.files;
        if (files && files.length > 0) {
          onDropFiles(files);
        }
      }}
    >
      <div className="relative flex min-h-0 flex-1 flex-col">
        {children}
        {active ? (
          <div
            className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-card border-2 border-dashed border-sky-300/60 bg-sky-400/10"
            data-testid="file-drop-overlay"
            role="status"
          >
            <span className="rounded-chip bg-surface/90 px-2 py-1 app-text-11 text-foreground shadow">
              {hint}
            </span>
          </div>
        ) : null}
      </div>
    </div>
  );
}
