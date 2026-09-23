// 「会话详情」面内「路由」区块的数据 hook：读取会话路由投影（GET /routing），
// 并按写入层提交补丁（PATCH /routing）。
//
// 语义边界（I-6）：hook 不加工任何路由字段——snapshot 原样保存后端响应，
// 层可写性、目标路径、warnings 全部来自 `panel`/`routing` 投影，前端不推断。
//
// 并发口径与 use-session-detail 一致：请求序号失效，旧响应不覆盖新状态；
// 卸载时中止在途请求；会话切换时清空投影，在途写入的响应按 sid 判定丢弃。
// 写入不做乐观更新——保存后以服务端返回的新投影为准。

import { useCallback, useEffect, useRef, useState } from "react";

import {
  getSessionRouting,
  updateSessionRouting,
} from "@/api/runtime/session-routing";
import { RuntimeApiError } from "@/api/runtime/shared";
import { readAdminToken } from "@/lib/admin-token";
import type {
  SessionRoutingMainAgentPatch,
  SessionRoutingResponse,
  SessionRoutingTargetLayer,
} from "@/types/runtime";

export type SessionRoutingWriteInput = {
  targetLayer: SessionRoutingTargetLayer;
  /** true=清除该层覆盖（reset 语义）；与 mainAgent/clearFields 互斥。 */
  clear?: boolean;
  /** config 层写入必须为 true（§5.4 二次确认）。 */
  confirm?: boolean;
  mainAgent?: SessionRoutingMainAgentPatch;
  clearFields?: string[];
};

export type SessionRoutingWriteOutcome =
  | { ok: true; snapshot: SessionRoutingResponse }
  /**
   * stale=true：写入结果属于已切走的会话（或被更新的写入取代），
   * 调用方应静默丢弃——不展示错误，也不改动当前界面。
   */
  | { ok: false; error: string; stale?: boolean };

function resolveErrorMessage(error: unknown): string {
  if (error instanceof RuntimeApiError) {
    return error.message;
  }
  if (error instanceof Error && error.message.trim()) {
    return error.message;
  }
  return "";
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

export function useSessionRouting(sessionId: string) {
  const [snapshot, setSnapshot] = useState<SessionRoutingResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seqRef = useRef(0);
  const writeSeqRef = useRef(0);
  const sid = sessionId?.trim() ?? "";
  // sid 的 ref 镜像：写入响应回来时用它判定「是否还停留在原会话」。
  const sidRef = useRef(sid);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!sid) {
        setSnapshot(null);
        setLoading(false);
        setError(null);
        return;
      }

      const seq = ++seqRef.current;
      setLoading(true);
      setError(null);
      try {
        const next = await getSessionRouting(sid, {
          adminToken: readAdminToken(),
          ...(signal ? { signal } : {}),
        });
        if (seq !== seqRef.current) {
          return;
        }
        setSnapshot(next);
      } catch (err) {
        if (seq !== seqRef.current || isAbortError(err)) {
          return;
        }
        setError(resolveErrorMessage(err));
      } finally {
        if (seq === seqRef.current) {
          setLoading(false);
        }
      }
    },
    [sid],
  );

  useEffect(() => {
    sidRef.current = sid;
    // 会话切换：立刻丢弃上一个会话的投影与错误——否则新会话加载失败时，
    // 界面会继续显示旧会话数据且错误不可见。
    seqRef.current += 1;
    setSnapshot(null);
    setError(null);
    setSaving(false);

    if (!sid) {
      setLoading(false);
      return;
    }

    const controller = new AbortController();
    void load(controller.signal);
    return () => {
      controller.abort();
    };
  }, [load, sid]);

  const reload = useCallback(async () => {
    await load();
  }, [load]);

  const write = useCallback(
    async (input: SessionRoutingWriteInput): Promise<SessionRoutingWriteOutcome> => {
      if (!sid) {
        return { ok: false, error: "" };
      }

      const writeSeq = ++writeSeqRef.current;
      setSaving(true);
      try {
        const next = await updateSessionRouting(
          sid,
          {
            target_layer: input.targetLayer,
            ...(input.clear ? { clear: true } : {}),
            ...(input.confirm ? { confirm: true } : {}),
            ...(input.mainAgent ? { main_agent: input.mainAgent } : {}),
            ...(input.clearFields && input.clearFields.length > 0
              ? { clear_fields: input.clearFields }
              : {}),
            updated_by: "web",
          },
          { adminToken: readAdminToken() },
        );
        // 会话已切走，或已有更新的写入：丢弃该响应。不拦的话，旧会话的投影
        // 会覆盖新会话界面，并作废新会话在途的 GET。
        if (sidRef.current !== sid || writeSeqRef.current !== writeSeq) {
          return { ok: false, error: "", stale: true };
        }
        // 写入后以服务端返回的投影为准（含 target_path / actor_invalidated / warnings）。
        // 同时递增请求序号：在途 GET 的结果早于本次写入，不得再回写。
        seqRef.current += 1;
        setSnapshot(next);
        setError(null);
        return { ok: true, snapshot: next };
      } catch (err) {
        if (sidRef.current !== sid) {
          return { ok: false, error: "", stale: true };
        }
        return { ok: false, error: resolveErrorMessage(err) };
      } finally {
        if (writeSeqRef.current === writeSeq) {
          setSaving(false);
        }
      }
    },
    [sid],
  );

  return {
    snapshot,
    loading,
    saving,
    error,
    reload,
    write,
  };
}
