// C5：把 tool_result 文本里运行时附加的指针行
// `Full raw output artifact_id: art_<32位hex>`
// 渲染为可点击的「查看完整原始输出」元素（匹配工具见 artifact-output-patterns.ts）。
//
// 点击优先级：
//   a. 宿主传入 onSelectArtifact → 调用它打开 artifact 详情对话框（id 为 art_ 后的完整 id）；
//   b. 否则把完整 id 复制到剪贴板并给出可见反馈（按钮文本切换「已复制」+ aria-live 播报），
//      绝不无响应。

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { CheckIcon, ClipboardIcon, FileSearchIcon } from "lucide-react";
import { cn } from "@/lib/utils";

const ARTIFACT_LINK_CLASS_NAME =
  "inline-flex items-center gap-1.5 rounded-field border border-border bg-surface-solid px-2.5 py-1.5 app-text-12 font-medium text-accent-secondary transition hover:border-border-strong hover:bg-surface-soft hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-secondary";

async function copyArtifactId(artifactId: string): Promise<boolean> {
  if (typeof navigator === "undefined" || !navigator.clipboard) {
    return false;
  }
  try {
    await navigator.clipboard.writeText(artifactId);
    return true;
  } catch {
    return false;
  }
}

export function ArtifactOutputLink({
  artifactId,
  className,
  onSelectArtifact,
}: {
  artifactId: string;
  className?: string;
  onSelectArtifact?: (artifactId: string) => void;
}) {
  const { t } = useTranslation("workspace");
  const [feedback, setFeedback] = useState<"copied" | "failed" | null>(null);
  const resetTimerRef = useRef<ReturnType<typeof window.setTimeout> | null>(
    null,
  );

  useEffect(() => {
    return () => {
      if (resetTimerRef.current !== null) {
        window.clearTimeout(resetTimerRef.current);
      }
    };
  }, []);

  function announce(next: "copied" | "failed") {
    setFeedback(next);
    if (resetTimerRef.current !== null) {
      window.clearTimeout(resetTimerRef.current);
    }
    resetTimerRef.current = window.setTimeout(() => {
      setFeedback(null);
      resetTimerRef.current = null;
    }, 1600);
  }

  async function handleClick() {
    if (onSelectArtifact) {
      // 宿主回调优先：打开 artifact 详情对话框。
      onSelectArtifact(artifactId);
      return;
    }
    // 兜底：复制完整 id，无论成败都给出可见反馈（aria-live），绝不无响应。
    const ok = await copyArtifactId(artifactId);
    announce(ok ? "copied" : "failed");
  }

  return (
    <button
      aria-label={t("panels.messages.markdown.openArtifactOutputAriaLabel", {
        artifactId,
      })}
      className={cn(ARTIFACT_LINK_CLASS_NAME, className)}
      data-artifact-output-link="true"
      onClick={handleClick}
      type="button"
    >
      {feedback === "copied" ? (
        <CheckIcon aria-hidden="true" size={14} />
      ) : (
        <FileSearchIcon aria-hidden="true" size={14} />
      )}
      {feedback === "copied"
        ? t("panels.messages.markdown.artifactOutputCopied")
        : t("panels.messages.markdown.openArtifactOutput")}
      <span
        aria-live="polite"
        className="sr-only"
        role="status"
      >
        {feedback === "copied"
          ? t("panels.messages.markdown.artifactOutputCopied", { artifactId })
          : feedback === "failed"
            ? t("panels.messages.markdown.artifactOutputCopyFailed", {
                artifactId,
              })
            : ""}
      </span>
      {feedback === "failed" ? (
        <ClipboardIcon aria-hidden="true" className="text-accent-orange" size={14} />
      ) : null}
    </button>
  );
}