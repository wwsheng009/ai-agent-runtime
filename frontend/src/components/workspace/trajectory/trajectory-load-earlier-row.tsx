/**
 * 轨迹列表顶部的「加载更早」入口（P0-2 拆文件）：尾部优先回放时，首屏只回放
 * 最近一页，更早内容从这里（或滚到顶端）向前翻页；已到日志开头则不占位。
 *
 * 与对话面同一处坑：轨迹列表实测可达 8.6 万 px（2,537 行），入口只有 sticky 常驻
 * 才可能被用户看到——流内首行等于永远看不见。
 */
import { useTranslation } from "react-i18next";

export function TrajectoryLoadEarlierRow({
  visible,
  loading,
  onLoad,
}: {
  /** 还有更早内容 / 正在加载才渲染（无更早页时不占位）。 */
  visible: boolean;
  /** 在途：禁用按钮并切换到「正在加载」文案。 */
  loading: boolean;
  onLoad?: () => void;
}) {
  const { t } = useTranslation("workspace");
  if (!visible) {
    return null;
  }
  return (
    <div
      className="sticky top-0 z-10 flex items-center justify-center bg-surface-softer/90 py-2 backdrop-blur-sm"
      data-trajectory-load-earlier="true"
    >
      <button
        className="rounded-full border border-border/60 px-3 py-1 app-text-11 text-muted-foreground transition hover:border-accent-teal hover:text-foreground disabled:cursor-default disabled:opacity-60"
        disabled={loading}
        onClick={() => onLoad?.()}
        type="button"
      >
        {loading
          ? t("panels.shell.trajectory.loadingEarlier")
          : t("panels.shell.trajectory.loadEarlier")}
      </button>
    </div>
  );
}
