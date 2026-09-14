// P2-7：composer 命令执行结果通知条（宿主已本地化文案；错误 alert、成功 status）。
//
// 从 `message-composer.tsx` 抽出：该文件受行数门禁（≤ 500 非空行）约束，
// 通知条是一段自洽的原子呈现，放这里也让「命令结果」与「菜单提示」在结构上并列。

import { cn } from "@/lib/utils";

export type ComposerCommandResultNotice = {
  tone: "success" | "error";
  text: string;
};

export function ComposerCommandResultNoticeBar({
  dismissLabel,
  notice,
  onDismiss,
}: {
  /** 「关闭提示」等本地化文案（模型层只持 i18n key，渲染层本地化）。 */
  dismissLabel: string;
  notice: ComposerCommandResultNotice;
  onDismiss?: () => void;
}) {
  return (
    <div
      role={notice.tone === "error" ? "alert" : "status"}
      data-composer-command-result={notice.tone}
      className={cn(
        "flex items-start justify-between gap-2 px-3 pt-2 app-text-10",
        notice.tone === "error" ? "text-[#d8a66d]" : "text-muted-foreground",
      )}
    >
      <span>{notice.text}</span>
      {onDismiss ? (
        <button
          type="button"
          data-composer-command-result-dismiss
          onClick={onDismiss}
          className="shrink-0 underline-offset-2 hover:text-foreground"
        >
          {dismissLabel}
        </button>
      ) : null}
    </div>
  );
}
