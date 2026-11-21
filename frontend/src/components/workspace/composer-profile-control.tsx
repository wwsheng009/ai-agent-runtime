import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangleIcon } from "lucide-react";

import type { SessionProfileSwitchReport } from "@/api/runtime/profiles";
import { useComposerSurface } from "@/components/workspace/composer-surface-store";
import { Select } from "@/components/ui/select";
import { composerProfileCandidates } from "@/lib/composer-profile-options";
import type { RuntimeProfileListResponse } from "@/types/runtime";

// Composer profile 切换器（context 用量图标左侧的常驻座位）。
//
// 职责边界（与 ComposerPermissionModeControl 同纪律）：
// - 只做「渲染 + 回调」：当前绑定真值来自会话 metadata（profileRef / profileName，
//   后端 set_profile 写入 sessionmeta 后随会话列表回读）；切换动作复用宿主的
//   `setSessionProfile` 处理器（与 `/profile` 命令同一执行核心），pending 与
//   失败原因由本组件如实呈现，不伪造「已切换」；
// - 候选来自运行时目录（只含可解析项）；目录未就绪/失败时如实进入空态，
//   不伪造候选；
// - 当前绑定的 ref 不在目录里（如 profile 已删除）时仍如实显示并标注，
//   而不是把触发器退回 placeholder；
// - 切换成功后本地采用 report.to 作为当前值（下一轮生效的权威事实），
//   会话列表回读（profile_ref）追上后自动收敛为服务端真值；
// - 新会话未登记时仅更新页面级 pending profile，不调用 session switch API。
type ComposerProfileControlProps = {
  /** 当前会话 id；新会话尚未登记时为空。 */
  sessionId?: string;
  /** 只有明确的新线程才启用本地 pending 分支。 */
  isNewThread?: boolean;
  /** 会话绑定的 profile 引用（metadata.context.profile_ref）。 */
  profileRef?: string;
  /** 绑定 profile 的显示名（profile_name；缺失时回退 profileRef）。 */
  profileName?: string;
  /** 运行时 profile 目录（宿主持有的唯一事实源；null = 未就绪）。 */
  catalog: RuntimeProfileListResponse | null;
  /** 目录加载失败原因（目录就绪时为 null）。 */
  catalogError?: string | null;
  /** 切换动作（宿主 handleProfileSwitch）；失败时抛错，组件如实呈现。 */
  onSwitch: (profileRef: string) => Promise<SessionProfileSwitchReport>;
  /** 新线程首轮尚未有 session 时的本地选择。 */
  pendingProfileRef?: string;
  onPendingProfileChange?: (profileRef: string) => void;
};

export function ComposerProfileControl({
  sessionId,
  profileRef,
  profileName,
  catalog,
  catalogError = null,
  isNewThread = false,
  onSwitch,
  onPendingProfileChange,
  pendingProfileRef = "",
}: ComposerProfileControlProps) {
  const { t } = useTranslation("workspace");
  const surface = useComposerSurface("profile");
  // 候选 / 能力广告 / 加载态都派生自同一份目录，不另设第二数据源。
  const candidates = useMemo(() => composerProfileCandidates(catalog), [catalog]);
  const selectable = useMemo(
    () => candidates.filter((candidate) => candidate.valid),
    [candidates],
  );
  const newThread = isNewThread && !sessionId;
  const switchSupported = newThread
    ? selectable.length > 0
    : catalog?.sessionSwitch === true;
  const catalogLoading = !catalog && !catalogError;
  const [pendingRef, setPendingRef] = useState("");
  const [error, setError] = useState<string | null>(null);
  // 切换成功后的本地真值（report.to），覆盖列表回读的旧快照。
  const [appliedRef, setAppliedRef] = useState<{ sessionId: string; ref: string } | null>(
    null,
  );

  const sessionProfileRef = (profileRef ?? "").trim();
  const sessionProfileName = (profileName ?? "").trim();
  // 会话切换后丢弃旧会话的本地覆盖；列表回读到同名 ref 后同样收敛。
  const appliedOverride =
    appliedRef && appliedRef.sessionId === sessionId && appliedRef.ref !== sessionProfileRef
      ? appliedRef.ref
      : "";
  // set_profile 写入的 profile_ref 可能是解析后的绝对路径（root/layer 来源），
  // 而目录候选 ref 是名字：先按 profile_ref 匹配，再按 profile_name 兜底，
  // 都不命中时才视为「不在目录中」。
  const boundCandidate = useMemo(() => {
    if (selectable.length === 0 && candidates.length === 0) {
      return null;
    }
    return (
      candidates.find((item) => item.ref === sessionProfileRef) ??
      candidates.find((item) => item.ref === sessionProfileName) ??
      null
    );
  }, [candidates, selectable.length, sessionProfileName, sessionProfileRef]);
  const pendingProfile = pendingProfileRef.trim();
  const defaultCandidate = candidates.find((candidate) => candidate.isDefault && candidate.valid);
  const currentRef = newThread
    ? pendingProfile || defaultCandidate?.ref || ""
    : appliedOverride || boundCandidate?.ref || sessionProfileRef;

  // 会话本身切换时清掉残留的错误与 pending 视图。
  useEffect(() => {
    setError(null);
    setPendingRef("");
  }, [sessionId]);

  const currentCandidate = newThread
    ? candidates.find((candidate) => candidate.ref === currentRef)
    : boundCandidate;
  const currentLabel = currentCandidate?.label || sessionProfileName || currentRef;

  const options = useMemo(() => {
    const items = selectable.map((candidate) => ({
      value: candidate.ref,
      label: candidate.isDefault
        ? t("composer.profile.optionDefault", { label: candidate.label })
        : candidate.label,
    }));
    // 当前绑定不在可选集合（已删除 / 目录未含 / 不可解析）：如实补一行展示项。
    if (currentRef.length > 0 && !selectable.some((item) => item.ref === currentRef)) {
      items.unshift({
        value: currentRef,
        label: t("composer.profile.optionUnknown", { label: currentLabel }),
      });
    }
    return items;
  }, [currentLabel, currentRef, selectable, t]);

  if (!sessionId && !newThread) {
    return null;
  }

  const busy = pendingRef.length > 0;
  const disabled =
    !switchSupported || busy || catalogLoading || (options.length === 0 && currentRef.length === 0);
  const currentValueLabel = currentLabel || t("composer.profile.unbound");
  const controlTitle = [
    t("composer.profile.controlTitle"),
    currentValueLabel,
    catalogError
      ? `${t("composer.profile.catalogError")}: ${catalogError}`
      : null,
  ]
    .filter(Boolean)
    .join(" · ");

  const handleSelect = (nextRef: string) => {
    if (nextRef === currentRef || busy) {
      return;
    }
    if (newThread) {
      setError(null);
      onPendingProfileChange?.(nextRef);
      return;
    }
    if (!sessionId) {
      return;
    }
    setPendingRef(nextRef);
    setError(null);
    void (async () => {
      try {
        const report = await onSwitch(nextRef);
        if (!report) {
          throw new Error("profile switch was rejected by the runtime");
        }
        // 本地采纳用户点选的目录 ref（report.to 可能是解析后的绝对路径，
        // 与目录 ref 词汇不同；列表回读 profile_ref/profile_name 后自动收敛）。
        setAppliedRef({ sessionId, ref: nextRef });
      } catch (switchError) {
        setError(
          switchError instanceof Error && switchError.message.trim().length > 0
            ? switchError.message
            : t("composer.profile.switchFailed"),
        );
      } finally {
        setPendingRef("");
      }
    })();
  };

  return (
    <div
      className="flex min-w-0 shrink-0 items-center gap-1.5"
      title={controlTitle}
    >
      <Select
        className="min-w-0 shrink-0"
        ariaLabel={t("composer.profile.controlLabel")}
        open={surface.open}
        onOpenChange={(next) => {
          if (next) {
            surface.openSelf();
          } else {
            surface.closeSelf();
          }
        }}
        value={currentRef}
        onChange={handleSelect}
        options={options}
        disabled={disabled}
        placeholder={t("composer.profile.unbound")}
        side="top"
        align="end"
        size="sm"
        title={controlTitle}
        triggerPrefix={t("composer.profile.controlLabel")}
        triggerValueClassName="min-w-[3.5rem]"
        triggerClassName="h-8 w-[clamp(9rem,12vw,10rem)] min-w-[9rem] max-w-full rounded-field px-1.5 py-0 text-sm leading-none normal-case tracking-normal disabled:opacity-80"
        menuClassName="max-w-[16rem]"
        optionClassName="text-base"
      />
      <span
        data-composer-profile-ref={currentRef}
        data-composer-profile-supported={switchSupported ? "true" : "false"}
        data-composer-profile-pending={pendingRef || undefined}
        className="sr-only"
      >
        {currentValueLabel}
      </span>
      {busy ? (
        <span
          data-composer-profile-busy
          className="min-w-0 truncate whitespace-nowrap text-muted-foreground"
        >
          {t("composer.profile.pending")}
        </span>
      ) : null}
      {error ? (
        <span
          data-composer-profile-error
          className="min-w-0 truncate whitespace-nowrap text-accent-gold"
          title={error}
        >
          {t("composer.profile.switchFailed")}
        </span>
      ) : null}
      {!busy && !error && catalogError ? (
        <span
          data-composer-profile-error
          className="inline-flex size-4 shrink-0 items-center justify-center text-accent-gold"
          title={`${t("composer.profile.catalogError")}: ${catalogError}`}
          aria-label={`${t("composer.profile.catalogError")}: ${catalogError}`}
          role="img"
        >
          <AlertTriangleIcon size={13} aria-hidden="true" />
        </span>
      ) : null}
      {!busy && !error && !catalogError && catalog !== null && !switchSupported ? (
        <span
          data-composer-profile-unsupported
          className="min-w-0 truncate whitespace-nowrap text-muted-foreground"
        >
          {t("composer.profile.unsupported")}
        </span>
      ) : null}
    </div>
  );
}
