/**
 * 对话面消息流顶部的「加载更早」入口（会话历史尾部优先分页）：首屏只取最新一页
 * （后端 /history 默认 100 条），更早内容从这里或滚到顶端向前翻页；已到开头则
 * 不占位（`hasMore=false` 时整体不渲染，空列表也不会多出一行）。
 *
 * 贴顶常驻（sticky）是必须的：一页 100 条实测约 1.2 万 px 高，入口若只做「流内首行」，
 * 用户停在贴底位置时它在视口上方 7,600+px（7,631 条会话实测），等于看不见——
 * 这正是「没有『加载更早』按钮」的直接原因。
 */
import { useTranslation } from "react-i18next";

export function LoadEarlierRow({
  visible,
  loading,
  onLoad,
}: {
  /** 还有更早历史 / 正在加载才渲染。 */
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
      className="sticky top-0 z-10 flex items-center justify-center bg-surface-softer/90 py-1 backdrop-blur-sm"
      data-message-load-earlier="true"
    >
      <button
        className="rounded-full border border-border/60 px-3 py-1 app-text-11 text-muted-foreground transition hover:border-accent-teal hover:text-foreground disabled:cursor-default disabled:opacity-60"
        disabled={loading}
        onClick={() => onLoad?.()}
        type="button"
      >
        {loading
          ? t("panels.messages.messageList.loadingEarlier")
          : t("panels.messages.messageList.loadEarlier")}
      </button>
    </div>
  );
}
