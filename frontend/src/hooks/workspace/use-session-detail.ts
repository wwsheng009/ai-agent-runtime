// 「会话详情」面（右侧栏）的数据解析：单个运行时会话快照的读取与刷新。
//
// 为什么不下沉到 PanelHost：详情数据只服务于该面本身，不参与页签徽标，
// 也不与计划 / 还原点共享解析结果，因此按自包含面契约（只收 sessionId）自行取数。
//
// 状态口径（对齐 use-session-runtime-stream：effect 同步段不写状态）：
//   * 快照按「会话 id」存储，渲染期派生——id 未变才采用，否则一律呈现 loading，
//     不在 effect 里同步 setState（react-hooks/set-state-in-effect）；
//   * 请求序号作废在途回包：会话切换 / 刷新后，旧 id 的迟到响应一律丢弃，不虚假回填。
//
// 刷新策略（不引入轮询，避免空闲会话持续打点）：
//   * `sessionId` 变化 → 立即重取；
//   * 手动 `reload()`（点击事件内标记刷新中，保留旧数据避免闪烁）；
//   * 页面重新可见（切回标签页）时静默重取一次，避免关闭/归档等状态长期陈旧。

import { useCallback, useEffect, useRef, useState } from "react";

import { getRuntimeSession } from "@/lib/runtime-api";
import { type RuntimeSessionRecord } from "@/types/runtime";

export type SessionDetailStatus = "idle" | "loading" | "ready" | "error";

export type UseSessionDetailResult = {
  /** 读取失败时的原始信息；成功或空闲时为 null。 */
  error: string | null;
  /** 重新拉取（错误态重试与手动刷新共用）。 */
  reload: () => void;
  /** 会话快照；未附着 / 未加载完成时为 null。 */
  session: RuntimeSessionRecord | null;
  status: SessionDetailStatus;
};

type SessionDetailSnapshot = {
  /** 快照归属的会话 id；与当前 id 不一致即为陈旧数据，渲染期不采用。 */
  sessionId: string;
  status: Exclude<SessionDetailStatus, "idle">;
  session: RuntimeSessionRecord | null;
  error: string | null;
};

const EMPTY_SNAPSHOT: SessionDetailSnapshot = {
  sessionId: "",
  status: "loading",
  session: null,
  error: null,
};

export function useSessionDetail(sessionId: string): UseSessionDetailResult {
  const normalizedSessionId = sessionId.trim();
  const [snapshot, setSnapshot] = useState<SessionDetailSnapshot>(EMPTY_SNAPSHOT);
  // 请求序号：会话切换 / 刷新后，旧请求的回包一律丢弃。
  const requestSeq = useRef(0);

  const fetchSnapshot = useCallback((id: string) => {
    if (!id) {
      return;
    }
    const seq = ++requestSeq.current;
    // 状态写入全部发生在 await 之后（异步续体），effect 同步段保持无副作用。
    void (async () => {
      try {
        const response = await getRuntimeSession(id);
        if (requestSeq.current !== seq) {
          return;
        }
        setSnapshot({
          sessionId: id,
          status: "ready",
          session: response.session ?? null,
          error: null,
        });
      } catch (cause: unknown) {
        if (requestSeq.current !== seq) {
          return;
        }
        setSnapshot({
          sessionId: id,
          status: "error",
          session: null,
          error: cause instanceof Error ? cause.message : String(cause),
        });
      }
    })();
  }, []);

  useEffect(() => {
    if (normalizedSessionId) {
      fetchSnapshot(normalizedSessionId);
    }
    return () => {
      // 会话切换 / 卸载：作废在途请求，避免旧会话数据落到新会话上。
      requestSeq.current += 1;
    };
  }, [fetchSnapshot, normalizedSessionId]);

  useEffect(() => {
    if (!normalizedSessionId) {
      return;
    }
    const handleVisibilityChange = () => {
      if (document.visibilityState === "visible") {
        // 静默重取：不改 status，避免回到前台时闪一下 loading。
        fetchSnapshot(normalizedSessionId);
      }
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
    };
  }, [fetchSnapshot, normalizedSessionId]);

  const reload = useCallback(() => {
    const id = normalizedSessionId;
    if (!id) {
      return;
    }
    // 点击事件（非 effect 同步段）标记刷新中：保留旧数据，只让刷新按钮转起来。
    setSnapshot((previous) => ({
      ...previous,
      sessionId: id,
      status: "loading",
      error: null,
    }));
    fetchSnapshot(id);
  }, [fetchSnapshot, normalizedSessionId]);

  // 渲染期派生：只采用与当前会话 id 匹配的快照，其余一律按「加载中」呈现。
  const isCurrent = Boolean(normalizedSessionId) && snapshot.sessionId === normalizedSessionId;

  return {
    error: isCurrent ? snapshot.error : null,
    reload,
    session: isCurrent ? snapshot.session : null,
    status: !normalizedSessionId ? "idle" : isCurrent ? snapshot.status : "loading",
  };
}
