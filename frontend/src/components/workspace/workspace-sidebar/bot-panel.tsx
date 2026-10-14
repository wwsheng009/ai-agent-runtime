// 侧栏「机器人」面板（docs/bots/09-multi-instance-binding.md §5.3 N4）：
// 列出连接器 + 绑定摘要，提供「绑定到当前会话 / 切换到当前会话 / 解绑」。
//
// 交互约定：
//   - 目标会话 = 侧栏当前选中会话（selectedSessionId）；未选中时绑定按钮禁用；
//   - 对已绑定 chat：「切换到当前会话」= bind 覆盖式改指（乐观锁带 updated_at）；
//   - 「绑定新 chat」展开 chat 输入框（进阶：把一个新 IM 会话指到当前 agent 会话）；
//   - 解绑 = DELETE 墓碑，旧会话历史保留；
//   - 面板数据自治（use-workspace-bot-panel），加载失败只影响本面板。

import { MessageSquareIcon, PlusIcon, RefreshCwIcon, UnlinkIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import {
  useWorkspaceBotPanel,
} from "@/hooks/workspace/use-workspace-bot-panel";

const inputClassName =
  "h-8 w-full rounded-control border border-border bg-surface-soft px-2 text-xs text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary";

interface WorkspaceBotPanelProps {
  /** 当前选中的 runtime 会话 id（绑定目标）；空 = 无可绑定目标。 */
  selectedSessionId: string;
}

export function WorkspaceBotPanel({ selectedSessionId }: WorkspaceBotPanelProps) {
  const { t } = useTranslation("workspace");
  const panel = useWorkspaceBotPanel();
  const [addingFor, setAddingFor] = useState<string | null>(null);
  const [newChat, setNewChat] = useState("");

  const canBind = selectedSessionId.trim().length > 0;

  const handleBindExisting = async (connectorId: string, chat: string) => {
    const ok = await panel.bind(connectorId, chat, selectedSessionId.trim());
    if (ok) {
      setAddingFor(null);
      setNewChat("");
    }
  };

  const handleBindNew = async (connectorId: string) => {
    const chat = newChat.trim();
    if (!chat) {
      return;
    }
    const ok = await panel.bind(connectorId, chat, selectedSessionId.trim());
    if (ok) {
      setAddingFor(null);
      setNewChat("");
    }
  };

  return (
    <div className="mt-3 rounded-card border border-border bg-surface-solid p-2.5">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
          {t("sidebar.bots.title")}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-1.5"
          onClick={panel.refresh}
          disabled={panel.refreshing}
          aria-label={t("sidebar.bots.refresh")}
        >
          <RefreshCwIcon size={12} className={cn(panel.refreshing && "animate-spin")} />
        </Button>
      </div>

      {panel.loading ? (
        <p className="app-text-10 text-muted-foreground">{t("sidebar.bots.loading")}</p>
      ) : panel.error ? (
        <p className="rounded-card border border-accent-orange/18 bg-accent-orange/8 px-2 py-1.5 text-xs text-muted-foreground">
          {t("sidebar.bots.loadFailed", { message: panel.error })}
        </p>
      ) : panel.items.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("sidebar.bots.empty")}</p>
      ) : (
        <ul className="space-y-1.5">
          {panel.items.map(({ connector, bindings }) => (
            <li
              key={connector.id}
              className="rounded-card border border-border bg-surface-soft px-2 py-1.5"
            >
              <div className="flex min-w-0 items-center justify-between gap-2">
                <span className="truncate text-xs font-medium text-foreground">
                  {connector.name}
                </span>
                <Badge>
                  {connector.active_owner
                    ? t("sidebar.bots.owner", { owner: connector.active_owner })
                    : t("sidebar.bots.unclaimed")}
                </Badge>
              </div>

              {bindings.length > 0 ? (
                <ul className="mt-1 space-y-1">
                  {bindings.map((binding) => {
                    const boundToCurrent =
                      selectedSessionId.trim() !== "" &&
                      binding.session_id === selectedSessionId.trim();
                    return (
                      <li
                        key={binding.chat}
                        className="flex items-center justify-between gap-1.5"
                      >
                        <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
                          <MessageSquareIcon size={11} />
                          <span className="truncate">{binding.chat}</span>
                          {boundToCurrent ? (
                            <span className="text-accent-primary">
                              {t("sidebar.bots.boundToCurrent")}
                            </span>
                          ) : null}
                        </span>
                        <span className="flex shrink-0 items-center gap-1">
                          {!boundToCurrent && canBind ? (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-6 px-1.5 text-xs"
                              disabled={panel.actionBusy !== null}
                              onClick={() =>
                                void handleBindExisting(connector.id, binding.chat)
                              }
                            >
                              {t("sidebar.bots.switchToCurrent")}
                            </Button>
                          ) : null}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-6 px-1.5 text-xs"
                            disabled={panel.actionBusy !== null}
                            onClick={() =>
                              void panel.unbind(connector.id, binding.chat)
                            }
                            aria-label={t("sidebar.bots.unbind")}
                          >
                            <UnlinkIcon size={11} />
                          </Button>
                        </span>
                      </li>
                    );
                  })}
                </ul>
              ) : (
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("sidebar.bots.noBindings")}
                </p>
              )}

              {addingFor === connector.id ? (
                <div className="mt-1.5 flex items-center gap-1">
                  <input
                    type="text"
                    className={inputClassName}
                    placeholder={t("sidebar.bots.chatPlaceholder")}
                    value={newChat}
                    onChange={(event) => setNewChat(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter" && newChat.trim() && canBind) {
                        void handleBindNew(connector.id);
                      }
                      if (event.key === "Escape") {
                        setAddingFor(null);
                        setNewChat("");
                      }
                    }}
                    autoFocus
                  />
                  <Button
                    variant="secondary"
                    size="sm"
                    className="h-8 shrink-0"
                    disabled={!newChat.trim() || !canBind || panel.actionBusy !== null}
                    onClick={() => void handleBindNew(connector.id)}
                  >
                    {t("sidebar.bots.bindNew")}
                  </Button>
                </div>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-1 h-6 px-1 text-xs text-muted-foreground"
                  disabled={!canBind || panel.actionBusy !== null}
                  onClick={() => {
                    setAddingFor(connector.id);
                    setNewChat("");
                  }}
                >
                  <PlusIcon size={11} />
                  {t("sidebar.bots.bindNewChat")}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {panel.actionError ? (
        <p className="mt-2 rounded-card border border-accent-orange/18 bg-accent-orange/8 px-2 py-1.5 text-xs text-muted-foreground">
          {t("sidebar.bots.actionFailed", { message: panel.actionError })}
        </p>
      ) : null}
      {!canBind ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {t("sidebar.bots.selectSessionHint")}
        </p>
      ) : null}
    </div>
  );
}
