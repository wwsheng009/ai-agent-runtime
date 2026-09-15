import { CheckIcon, CopyIcon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  codeHighlightingReady,
  highlightCode,
  isCodeHighlightingReady,
  plainCodeLines,
  supportsHighlighting,
} from "@/components/ui/code-highlighting";
import { useViewportActivation } from "@/components/ui/use-viewport-activation";
import { cn } from "@/lib/utils";

type CodeBlockProps = {
  code: string;
  language: string;
  title?: string;
  className?: string;
  collapsible?: boolean;
  collapseLineCount?: number;
  /**
   * 流式第二前沿：仍在增长的 partial 行（`code` 保持 `\n` 结尾的整行前缀）。
   * partial 行按纯文本渲染，不进入 Prism 输入——半行代码会得到错误 token
   * （例如未闭合字符串把后续内容整体染色），也避免整块随每个 chunk 重tokenize。
   */
  partialLine?: string;
  streaming?: boolean;
};

type CodeBlockSurfaceProps = Omit<
  CodeBlockProps,
  "collapseLineCount" | "collapsible" | "streaming"
> & {
  collapseLineCount: number;
  collapsible: boolean;
  streaming: boolean;
};

const DEFAULT_COLLAPSE_LINE_COUNT = 16;

export function CodeBlock({
  code,
  language,
  title,
  className,
  collapsible = false,
  collapseLineCount = DEFAULT_COLLAPSE_LINE_COUNT,
  partialLine,
  streaming = false,
}: CodeBlockProps) {
  return (
    <CodeBlockSurface
      className={className}
      code={code}
      collapsible={collapsible}
      collapseLineCount={collapseLineCount}
      language={language}
      partialLine={partialLine}
      streaming={streaming}
      title={title}
    />
  );
}

function CodeBlockSurface({
  code,
  language,
  title,
  className,
  collapsible,
  collapseLineCount,
  partialLine,
  streaming,
}: CodeBlockSurfaceProps) {
  const { t } = useTranslation("common");
  const [copied, setCopied] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [prismReady, setPrismReady] = useState(() => isCodeHighlightingReady());
  const highlightable = supportsHighlighting(language);
  // 语言可高亮时延迟到代码块进入视口再激活（激活后一次性停止观察）；
  // 环境不支持 IntersectionObserver 时立即激活，保持纯静态渲染可用。
  const { activated, targetRef } =
    useViewportActivation<HTMLDivElement>(highlightable);
  const highlightedLines = useMemo(
    () =>
      activated && prismReady && highlightable
        ? highlightCode(code, language)
        : plainCodeLines(code, language),
    [activated, code, highlightable, language, prismReady],
  );
  const resolvedCollapseLineCount =
    collapseLineCount ?? DEFAULT_COLLAPSE_LINE_COUNT;
  // `code`（= stableCode）保持 `\n` 结尾，最后一行切出来必然是空行；有 partial 行时
  // 这行是切分产物而不是真实内容，去掉它再补 partial 行，行号与内容才对得上。
  const stableLines =
    partialLine &&
    highlightedLines.length > 0 &&
    highlightedLines[highlightedLines.length - 1].segments.length === 0
      ? highlightedLines.slice(0, -1)
      : highlightedLines;
  const partialLineCount = partialLine ? 1 : 0;
  const totalLineCount = stableLines.length + partialLineCount;
  const canCollapse =
    collapsible &&
    !streaming &&
    totalLineCount > resolvedCollapseLineCount;
  const visibleLines = canCollapse && !expanded
    ? stableLines.slice(0, resolvedCollapseLineCount)
    : stableLines;
  const hiddenLineCount = totalLineCount - visibleLines.length - partialLineCount;
  const showPartialLine = Boolean(partialLine) && !canCollapse;

  useEffect(() => {
    if (prismReady) {
      return;
    }

    let active = true;
    void codeHighlightingReady.then(() => {
      if (active) {
        setPrismReady(true);
      }
    });

    return () => {
      active = false;
    };
  }, [prismReady]);

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(
        partialLine ? `${code}${partialLine}` : code,
      );
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div
      ref={targetRef}
      className={cn(
        "app-code-surface overflow-hidden rounded-panel border border-border bg-code-block-bg",
        className,
      )}
    >
      <div className="flex items-center justify-between border-b border-border bg-code-block-header-bg px-3 py-2">
        <div className="min-w-0">
          <div className="truncate app-text-13 font-semibold text-code-block-foreground">
            {title ?? t("codeBlock.fallbackTitle")}
          </div>
          <div className="mt-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {language}
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("codeBlock.copy")}
          onClick={handleCopy}
        >
          {copied ? <CheckIcon size={16} /> : <CopyIcon size={16} />}
        </Button>
      </div>
      <div className="overflow-x-auto px-0 py-2.5">
        <pre className="m-0 min-w-full px-0">
          {visibleLines.map((line, index) => (
            <div
              // key 只认位置 + 行种类，**不认内容**：把行内容 / 分词内容拼进 key（原实现）
              // 意味着 Prism 每重切一次尾部，相关行的整棵 div（行号 + <code> 容器）就被
              // 卸载重挂。实测流式期间单个 chunk 有 58 次 childList 落在 <code> 上
              // （e2e/zz-perf-probe.spec.ts → idspike-40），并连带整块样式失效与重排。
              key={`line-${index}-${line.kind}`}
              className="app-code-line grid grid-cols-[2.5rem_minmax(0,1fr)] gap-3 px-3 text-code-block-foreground"
              data-line-kind={line.kind === "normal" ? undefined : line.kind}
            >
              <span className="app-code-line-number select-none text-right font-mono text-code-line-number">
                {index + 1}
              </span>
              <code className="font-mono whitespace-pre">
                {line.segments.length === 0
                  ? " "
                  : line.segments.map((segment, segmentIndex) =>
                      segment.types.length > 0 ? (
                        <span
                          // 同理：分词内容变化只该改文本，不该换元素身份（换 key = 重挂）。
                          key={`segment-${segmentIndex}`}
                          className={cn("token", ...segment.types)}
                        >
                          {segment.content}
                        </span>
                      ) : (
                        <span key={`segment-${segmentIndex}`}>
                          {segment.content}
                        </span>
                      ),
                    )}
              </code>
            </div>
          ))}
          {showPartialLine ? (
            <div
              className="app-code-line grid grid-cols-[2.5rem_minmax(0,1fr)] gap-3 px-3 text-code-block-foreground"
              data-line-kind="partial"
            >
              <span className="app-code-line-number select-none text-right font-mono text-code-line-number">
                {stableLines.length + 1}
              </span>
              <code className="font-mono whitespace-pre">{partialLine}</code>
            </div>
          ) : null}
        </pre>
      </div>
      {canCollapse ? (
        <div className="border-t border-border bg-[linear-gradient(180deg,rgba(255,255,255,0.01),rgba(255,255,255,0.03))] px-3 py-2.5">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-11 text-muted-foreground">
              {expanded
                ? t("codeBlock.showingAll", { count: totalLineCount })
                : t("codeBlock.hiddenNote", { count: hiddenLineCount })}
            </div>
            <Button
              aria-expanded={expanded}
              className="shrink-0"
              size="sm"
              variant="secondary"
              onClick={() => setExpanded((current) => !current)}
            >
              {expanded
                ? t("codeBlock.collapse")
                : t("codeBlock.showMoreLines", { count: hiddenLineCount })}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
