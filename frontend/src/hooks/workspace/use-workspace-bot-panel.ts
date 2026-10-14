// 侧栏「机器人」面板的数据面（docs/bots/09-multi-instance-binding.md §5.3 N4）：
// 拉取连接器列表 + 绑定列表，暴露 绑定/解绑 动作与刷新。
//
// 设计边界：
//   - 数据自治：面板自己拉取（不与 connectors 页共享状态），加载失败降级为错误
//     文案，不阻塞侧栏其它分区；
//   - 绑定目标 = 当前会话的 runtime session id（由调用方传入 selectedSessionId；
//     空会话时按钮禁用）；
//   - bind 的 chat 来源：绑定的 IM chat 无法从浏览器得知，v1 用「该连接器当前
//     绑定列表中已存在的 chat 重新指向 / 或用户显式输入 chat」。为保持零输入，
//     面板提供两种动作：
//       1) 「切换到当前会话」——对已绑定的 chat 把 session 换成当前会话；
//       2) 「绑定新 chat」——展开一个小输入框填 chat id（进阶用法）；
//   - 解绑 = DELETE（墓碑），旧会话上下文保留。

import { useCallback, useEffect, useMemo, useState } from "react";

import {
  listConnectors,
} from "@/api/runtime/connectors";
import {
  bindConnectorSession,
  listConnectorBindings,
  unbindConnectorSession,
} from "@/api/runtime/connector-bindings";
import { readAdminToken } from "@/lib/admin-token";
import type {
  ConnectorBindingView,
  ConnectorView,
} from "@/types/runtime";

export type BotPanelItem = {
  connector: ConnectorView;
  bindings: ConnectorBindingView[];
};

export type BotPanelState = {
  items: BotPanelItem[];
  loading: boolean;
  refreshing: boolean;
  error: string | null;
  actionError: string | null;
  actionBusy: string | null;
  refresh: () => void;
  bind: (connectorId: string, chat: string, sessionId: string) => Promise<boolean>;
  unbind: (connectorId: string, chat: string) => Promise<boolean>;
};

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function useWorkspaceBotPanel(): BotPanelState {
  const [connectors, setConnectors] = useState<ConnectorView[]>([]);
  const [bindings, setBindings] = useState<ConnectorBindingView[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const adminToken = readAdminToken();

  const load = useCallback(
    async (mode: "initial" | "refresh") => {
      if (mode === "refresh") {
        setRefreshing(true);
      } else {
        setLoading(true);
      }
      setError(null);
      const [connectorResult, bindingResult] = await Promise.allSettled([
        listConnectors({ adminToken }),
        listConnectorBindings({ adminToken }),
      ]);
      if (connectorResult.status === "fulfilled") {
        setConnectors(connectorResult.value);
      }
      if (bindingResult.status === "fulfilled") {
        setBindings(bindingResult.value);
      }
      const failure =
        connectorResult.status === "rejected"
          ? errorText(connectorResult.reason)
          : bindingResult.status === "rejected"
            ? errorText(bindingResult.reason)
            : null;
      setError(failure);
      if (mode === "initial") {
        setLoading(false);
      }
      setRefreshing(false);
    },
    [adminToken],
  );

  useEffect(() => {
    void load("initial");
  }, [load]);

  const refresh = useCallback(() => {
    void load("refresh");
  }, [load]);

  const items = useMemo<BotPanelItem[]>(
    () =>
      connectors.map((connector) => ({
        connector,
        bindings: bindings.filter((binding) => binding.connector_id === connector.id),
      })),
    [connectors, bindings],
  );

  const bind = useCallback(
    async (connectorId: string, chat: string, sessionId: string): Promise<boolean> => {
      const busyKey = `bind:${connectorId}:${chat}`;
      setActionBusy(busyKey);
      setActionError(null);
      try {
        const existing = bindings.find(
          (binding) => binding.connector_id === connectorId && binding.chat === chat,
        );
        await bindConnectorSession(
          connectorId,
          chat,
          {
            sessionId,
            ...(existing?.updated_at ? { expectedUpdatedAt: existing.updated_at } : {}),
          },
          { adminToken },
        );
        await load("refresh");
        return true;
      } catch (caught) {
        setActionError(errorText(caught));
        return false;
      } finally {
        setActionBusy(null);
      }
    },
    [adminToken, bindings, load],
  );

  const unbind = useCallback(
    async (connectorId: string, chat: string): Promise<boolean> => {
      const busyKey = `unbind:${connectorId}:${chat}`;
      setActionBusy(busyKey);
      setActionError(null);
      try {
        await unbindConnectorSession(connectorId, chat, { adminToken });
        await load("refresh");
        return true;
      } catch (caught) {
        setActionError(errorText(caught));
        return false;
      } finally {
        setActionBusy(null);
      }
    },
    [adminToken, load],
  );

  return {
    items,
    loading,
    refreshing,
    error,
    actionError,
    actionBusy,
    refresh,
    bind,
    unbind,
  };
}
