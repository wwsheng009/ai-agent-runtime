// 绑定会话与节点展示（docs/bots/09 §5.3）：bot 的每个 IM 会话绑定指向哪个
// agent 会话（session）、由哪个 mesh 节点（node）在服务。无绑定时不渲染。
import { useTranslation } from "react-i18next";

import type { ConnectorBindingView } from "@/types/runtime";

export function ConnectorBindingsList({
  bindings,
}: {
  bindings: ConnectorBindingView[];
}) {
  const { t } = useTranslation("connectors");
  if (bindings.length === 0) {
    return null;
  }
  return (
    <div className="flex flex-col gap-0.5 text-xs text-muted-foreground">
      <span className="app-text-10 uppercase tracking-[0.14em]">
        {t("page.bindings")}
      </span>
      {bindings.map((binding) => (
        <span
          key={binding.chat}
          className="truncate"
          title={`${binding.chat} → ${binding.session_id || "-"}${
            binding.node_id ? ` @ ${binding.node_id}` : ""
          }`}
        >
          <span className="font-mono">{binding.chat}</span>
          {" → "}
          {binding.session_id
            ? t("page.bindingSession", { session: binding.session_id })
            : t("page.bindingUnbound")}
          {binding.node_id
            ? ` · ${t("page.bindingNode", { node: binding.node_id })}`
            : ""}
        </span>
      ))}
    </div>
  );
}