import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

// P2-7 附带整理：composer 状态条从 message-composer 抽出（行数门禁）。
// 只做「渲染 + 触发动作」：附件/命令行/响应态的真值仍由 owner 持有并作为 props 传入。
type ComposerStatusRowProps = {
  isCompact: boolean;
  /** 附件草稿计数：在途 / 未落定 / 已上传（S5：新增即上传）。 */
  uploadingAttachmentCount: number;
  unsettledAttachmentCount: number;
  uploadedAttachmentCount: number;
  rejectedAttachmentCount: number;
  onAcknowledgeRejections: () => void;
  isResponding: boolean;
  selectedArtifactCount: number;
  /** P1-4 子片 3：命令行状态（`/` 命令被识别但尚未提交）。 */
  isCommandLine: boolean;
  /** 命令被阻塞时的可见原因（提示条本体由 composer 渲染，这里只保证状态条占位一致）。 */
  hasCommandNotice: boolean;
  transport?: Thread["transport"];
};

export function ComposerStatusRow({
  isCompact,
  uploadingAttachmentCount,
  unsettledAttachmentCount,
  uploadedAttachmentCount,
  rejectedAttachmentCount,
  onAcknowledgeRejections,
  isResponding,
  selectedArtifactCount,
  isCommandLine,
  hasCommandNotice,
  transport,
}: ComposerStatusRowProps) {
  const { t } = useTranslation("workspace");
  const hasAttachments = uploadedAttachmentCount > 0 || unsettledAttachmentCount > 0;
  const visible =
    transport === "error" ||
    selectedArtifactCount > 0 ||
    isResponding ||
    hasAttachments ||
    rejectedAttachmentCount > 0 ||
    isCommandLine ||
    hasCommandNotice;
  if (!visible) {
    return null;
  }
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border px-3 app-text-10 uppercase tracking-[0.12em] text-muted-foreground",
        isCompact ? "py-1" : "py-1.5",
      )}
    >
      {transport === "error" ? (
        <span className="text-[#d8a66d]">{t("composer.transport.error")}</span>
      ) : null}
      {selectedArtifactCount > 0 ? (
        <span>{t("composer.filesCount", { count: selectedArtifactCount })}</span>
      ) : null}
      {isResponding ? (
        <span className="text-accent-secondary">
          {t("composer.responseActive")}
        </span>
      ) : null}
      {uploadingAttachmentCount > 0 ? (
        <span data-composer-attachments-uploading role="status">
          {t("composer.attachments.uploadingCount", {
            count: uploadingAttachmentCount,
          })}
        </span>
      ) : null}
      {unsettledAttachmentCount > 0 ? (
        <span data-composer-attachments-blocked className="text-[#d8a66d]">
          {t("composer.attachments.unsettledBlocked", {
            count: unsettledAttachmentCount,
          })}
        </span>
      ) : null}
      {uploadedAttachmentCount > 0 && unsettledAttachmentCount === 0 ? (
        <span data-composer-attachments-ready role="status">
          {t("composer.attachments.readyCount", {
            count: uploadedAttachmentCount,
          })}
        </span>
      ) : null}
      {rejectedAttachmentCount > 0 ? (
        <button
          type="button"
          data-composer-attachments-rejected
          onClick={onAcknowledgeRejections}
          className="text-left text-[#d8a66d] underline-offset-2 hover:underline"
        >
          {t("composer.attachments.rejected", {
            count: rejectedAttachmentCount,
          })}
        </button>
      ) : null}
      {isCommandLine ? (
        <span data-composer-command-line className="text-accent-secondary">
          {t("composer.commands.lineHint")}
        </span>
      ) : null}
    </div>
  );
}
