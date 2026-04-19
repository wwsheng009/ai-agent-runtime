import { CheckIcon, CopyIcon } from "lucide-react";
import { useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import { highlightCode } from "@/components/ui/code-highlighting";
import { cn } from "@/lib/utils";

type CodeBlockProps = {
  code: string;
  language: string;
  title?: string;
  className?: string;
  collapsible?: boolean;
  collapseLineCount?: number;
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
  streaming = false,
}: CodeBlockProps) {
  return (
    <CodeBlockSurface
      key={`${language}\u0000${title ?? ""}\u0000${code}`}
      className={className}
      code={code}
      collapsible={collapsible}
      collapseLineCount={collapseLineCount}
      language={language}
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
  streaming,
}: CodeBlockSurfaceProps) {
  const [copied, setCopied] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const resolvedCollapseLineCount =
    collapseLineCount ?? DEFAULT_COLLAPSE_LINE_COUNT;
  const highlightedLines = useMemo(
    () => highlightCode(code, language),
    [code, language],
  );
  const canCollapse =
    collapsible &&
    !streaming &&
    highlightedLines.length > resolvedCollapseLineCount;
  const visibleLines = canCollapse && !expanded
    ? highlightedLines.slice(0, resolvedCollapseLineCount)
    : highlightedLines;
  const hiddenLineCount = highlightedLines.length - visibleLines.length;

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div
      className={cn(
        "app-code-surface overflow-hidden rounded-[0.9rem] border border-[var(--border)] bg-[var(--code-block-bg)]",
        className,
      )}
    >
      <div className="flex items-center justify-between border-b border-[var(--border)] bg-[var(--code-block-header-bg)] px-3 py-2">
        <div className="min-w-0">
          <div className="truncate app-text-13 font-semibold text-[var(--code-block-foreground)]">
            {title ?? "Code snippet"}
          </div>
          <div className="mt-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {language}
          </div>
        </div>
        <Button variant="ghost" size="icon" aria-label="Copy code" onClick={handleCopy}>
          {copied ? <CheckIcon size={16} /> : <CopyIcon size={16} />}
        </Button>
      </div>
      <div className="overflow-x-auto px-0 py-2.5">
        <pre className="m-0 min-w-full px-0">
          {visibleLines.map((line, index) => (
            <div
              key={`${index}-${line.kind}-${line.segments.map((segment) => segment.content).join("")}`}
              className="app-code-line grid grid-cols-[2.5rem_minmax(0,1fr)] gap-3 px-3 text-[var(--code-block-foreground)]"
              data-line-kind={line.kind === "normal" ? undefined : line.kind}
            >
              <span className="app-code-line-number select-none text-right font-mono text-[var(--code-line-number)]">
                {index + 1}
              </span>
              <code className="font-mono whitespace-pre">
                {line.segments.length === 0
                  ? " "
                  : line.segments.map((segment, segmentIndex) =>
                      segment.types.length > 0 ? (
                        <span
                          key={`${segmentIndex}-${segment.content}`}
                          className={cn("token", ...segment.types)}
                        >
                          {segment.content}
                        </span>
                      ) : (
                        <span key={`${segmentIndex}-${segment.content}`}>
                          {segment.content}
                        </span>
                      ),
                    )}
              </code>
            </div>
          ))}
        </pre>
      </div>
      {canCollapse ? (
        <div className="border-t border-[var(--border)] bg-[linear-gradient(180deg,rgba(255,255,255,0.01),rgba(255,255,255,0.03))] px-3 py-2.5">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-11 text-[var(--muted-foreground)]">
              {expanded
                ? `Showing all ${highlightedLines.length} lines.`
                : `${hiddenLineCount} more lines hidden for readability.`}
            </div>
            <Button
              aria-expanded={expanded}
              className="shrink-0"
              size="sm"
              variant="secondary"
              onClick={() => setExpanded((current) => !current)}
            >
              {expanded ? "Collapse code" : `Show ${hiddenLineCount} more lines`}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
