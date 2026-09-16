// 工作区右侧栏「文件浏览器面」→ 文本预览（行号 + Prism 高亮，P2-3）。
//
// 后端契约：文本内容来自 `FsPreview.text`（已由后端解码；截断与否看 `truncated` / `limit_bytes`），
//   本组件只渲染字符串，不做字节解码（字节解码复用 `lib/file-preview/decode.ts`，不在此重复实现）。
//
// 归一化纪律：
//   * `\r\n` / `\r` 一律归一为 `\n` 后再切行，行号与后端「第 N 行」口径一致；
//   * 预览被截断时如实标注（复用调用方给出的 truncated 文案），不假装文件到底了。
//
// 降级判据（只高亮可视行）：
//   * 固定行高 20px + 可视窗口切片：只有窗口内的行进入 Prism（大文件不做整文件 tokenize）；
//   * Prism 语言包懒加载未就绪 / 语言不支持 → `plainCodeLines` 纯文本降级，行号保持不变；
//   * 全程不做 `dangerouslySetInnerHTML`：Prism 结果按 token 片段渲染为 <span>。
import { useEffect, useMemo, useRef, useState } from "react";

import {
  codeHighlightingReady,
  highlightCode,
  isCodeHighlightingReady,
  plainCodeLines,
  supportsHighlighting,
  type CodeHighlightLine,
} from "@/components/ui/code-highlighting";
import { cn } from "@/lib/utils";

const TEXT_ROW_HEIGHT = 20;
const TEXT_OVERSCAN = 8;

/** 归一化换行后切行（`\r\n` 与 `\r` 都算一次换行）。 */
function splitTextLines(text: string): string[] {
  return text.replace(/\r\n?/g, "\n").split("\n");
}

export type TextViewerProps = {
  text: string;
  /** Prism 语言标识（由 `guessPrismLanguage` 推导），未知语言按纯文本渲染。 */
  language: string;
  /** 后端给出的总行数（仅有 text 内容时可为空，不猜测）。 */
  totalLines?: number;
  truncated?: boolean;
  truncatedNote?: string;
  className?: string;
};

export function TextViewer({
  className,
  language,
  text,
  totalLines,
  truncated = false,
  truncatedNote,
}: TextViewerProps) {
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(320);
  const [highlightReady, setHighlightReady] = useState(() => isCodeHighlightingReady());

  useEffect(() => {
    if (highlightReady) {
      return;
    }
    let mounted = true;
    void codeHighlightingReady.then(() => {
      if (mounted) {
        setHighlightReady(true);
      }
    });
    return () => {
      mounted = false;
    };
  }, [highlightReady]);

  useEffect(() => {
    const node = viewportRef.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() => setViewportHeight(node.clientHeight || 320));
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  const lines = useMemo(() => splitTextLines(text), [text]);
  const firstVisible = Math.max(0, Math.floor(scrollTop / TEXT_ROW_HEIGHT) - TEXT_OVERSCAN);
  const visibleCount = Math.max(1, Math.ceil(viewportHeight / TEXT_ROW_HEIGHT)) + TEXT_OVERSCAN * 2;
  const sliceEnd = Math.min(lines.length, firstVisible + visibleCount);
  const visibleLines = useMemo(
    () => lines.slice(firstVisible, sliceEnd),
    [firstVisible, lines, sliceEnd],
  );
  const canHighlight = highlightReady && supportsHighlighting(language);
  const highlighted: CodeHighlightLine[] = useMemo(() => {
    const source = visibleLines.join("\n");
    return canHighlight ? highlightCode(source, language) : plainCodeLines(source, language);
  }, [canHighlight, language, visibleLines]);

  return (
    <div className={cn("flex min-h-0 flex-col", className)}>
      <div className="flex items-center gap-2 border-b border-border/60 px-2 py-1 text-[11px] text-muted-foreground">
        <span>{language}</span>
        <span>{(totalLines ?? lines.length).toLocaleString()}</span>
        {truncated && truncatedNote ? (
          <span className="ml-auto text-accent-gold" data-testid="text-viewer-truncated">
            {truncatedNote}
          </span>
        ) : null}
      </div>
      <div
        className="app-scrollbar min-h-0 flex-1 overflow-auto bg-surface-solid/40 font-mono text-[11px] leading-5"
        data-testid="text-viewer"
        onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
        ref={viewportRef}
      >
        <div aria-hidden style={{ height: firstVisible * TEXT_ROW_HEIGHT }} />
        {visibleLines.map((line, index) => {
          const lineNumber = firstVisible + index + 1;
          const highlightedLine = highlighted[index];
          return (
            <div className="flex" data-line={lineNumber} key={lineNumber} style={{ height: TEXT_ROW_HEIGHT }}>
              <span className="w-10 shrink-0 select-none pr-2 text-right text-muted-foreground/70">
                {lineNumber}
              </span>
              <span className="min-w-0 flex-1 whitespace-pre pr-2">
                {(highlightedLine?.segments ?? [{ content: line, types: [] }]).map((segment, segmentIndex) => (
                  <span className={cn("token", ...segment.types)} key={`${lineNumber}:${segmentIndex}`}>
                    {segment.content}
                  </span>
                ))}
              </span>
            </div>
          );
        })}
        <div aria-hidden style={{ height: (lines.length - sliceEnd) * TEXT_ROW_HEIGHT }} />
      </div>
    </div>
  );
}
