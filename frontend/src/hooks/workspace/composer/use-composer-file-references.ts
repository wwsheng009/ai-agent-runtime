// P0：composer `@` 工作区文件引用（首屏小批量，零后端改动）。
//
// 链路：fs/roots 解析作用域（优先会话根，按 sessionId 缓存/作废）→ fs/list 拉根目录首页
//      → 映射为 `workspace-files` 引用分组（宿主把它排在 artifacts 分组之前）。
//
// 纪律（对齐 use-file-browser.ts 的既有竞态口径）：
//   * sessionId 变化：作废在途请求、清空结果、重新解析/复用作用域；
//   * 请求序号 + AbortController：只有最新序号的响应写回；主动取消（AbortError）不落 error 态；
//   * 菜单关闭（enabled=false）不发请求、保留上次结果（输入不闪空）；
//   * fs/roots 不可用（404/405/501/503）或没有可用根 → group=null，宿主回退 artifacts 分组；
//   * 失败保留上一次成功结果，`status="error"` 供 UI 提示（不把陈旧结果伪装成成功）。
//
// P1（已落地）：query 非空分支包装共享 `useFsSearchQuery`（防抖 200ms / limit=20 / kinds=file），
//            远端结果以 `serverFiltered=true` 下发，禁止客户端二次 rankMatch；
//            P0 分支保持 `fs/list` 不变。
//
// P1 降级（§4.5.3 / §4.7.5）：搜索端点 404/405/501/503 → 本 hook 粘住「不可用」并回到 P0 首屏 +
// 客户端过滤（不重试、不报错噪音）；其它错误保留上次结果 + `status="error"`。

import { useEffect, useMemo, useRef, useState } from "react";

import { fetchFsListing } from "@/api/runtime/fs-list";
import { fetchFsRoots, isFsRootsUnavailable, pickDefaultRoot } from "@/api/runtime/fs-roots";
import { isAbortError } from "@/hooks/workspace/use-file-browser";
import {
  FS_SEARCH_DEBOUNCE_COMPOSER_MS,
  useFsSearchQuery,
} from "@/hooks/workspace/use-fs-search";
import {
  type ComposerReferenceGroup,
  type ComposerReferenceItem,
  type ComposerReferenceStatus,
} from "@/lib/composer-menu";
import { type FsEntry, type FsRoot, type FsSearchItem } from "@/types/runtime/fs-browser";

export const COMPOSER_FILE_REFERENCE_GROUP_ID = "workspace-files";

/** 首屏小批量条数（后端默认 200；这里显式收窄，菜单每组最多渲染 8 条）。 */
export const COMPOSER_FILE_REFERENCE_LIMIT = 20;

/** 作用域缓存 TTL：同一会话 5 分钟内复用解析结果，避免每次打开菜单都打 fs/roots。 */
export const COMPOSER_FILE_REFERENCE_ROOT_TTL_MS = 5 * 60 * 1000;

export type ComposerFileReferencesStatus = "idle" | "loading" | "ready" | "error";

export type ComposerFileReferencesLabels = {
  /** 分组名（工作区文件）。 */
  group: string;
  loading: string;
  empty: string;
  error: string;
  truncated: string;
};

export type ComposerFileReferencesOptions = {
  sessionId?: string | null;
  /** 当前 `@` 查询串；由菜单状态回调驱动（§4.5.2）。 */
  query: string;
  /** 菜单处于 references 模式且打开时才请求；关闭时保留上次结果并停止请求。 */
  enabled: boolean;
  /** 已本地化文案（模型/数据层不持有文案）。 */
  labels: ComposerFileReferencesLabels;
};

export type ComposerFileReferencesResult = {
  /** id=COMPOSER_FILE_REFERENCE_GROUP_ID；无可用作用域时为 null。 */
  group: ComposerReferenceGroup | null;
  status: ComposerFileReferencesStatus;
  hasMore: boolean;
  truncated: boolean;
  scope: string | null;
  error: unknown;
};

type CachedRoot = {
  root: FsRoot | null;
  unavailable: boolean;
  at: number;
};

type RootsState = {
  status: "loading" | "ready" | "unavailable" | "error";
  scope: string | null;
  error: unknown;
};

type ListState = {
  status: ComposerFileReferencesStatus;
  entries: FsEntry[];
  hasMore: boolean;
  truncated: boolean;
  error: unknown;
};

const IDLE_LIST_STATE: ListState = {
  status: "idle",
  entries: [],
  hasMore: false,
  truncated: false,
  error: null,
};

// 作用域缓存按会话 key 存放：会话切换立即失效在途请求，缓存只加速「重新打开菜单」。
// 条目 5 分钟 TTL 兜底：工作区注销/权限变化时即使没有显式清理，最坏情况是复用旧 scope
// 并被端点判为 scope_not_found，走既有降级/错误态（不静默）。
const rootCache = new Map<string, CachedRoot>();

/** 会话切换/登出时可显式清理（测试亦用它隔离用例）；未接入清理点时有 TTL 兜底。 */
export function clearComposerFileReferenceRootCache(): void {
  rootCache.clear();
}

function readCachedRoot(key: string): CachedRoot | null {
  const cached = rootCache.get(key);
  if (!cached) {
    return null;
  }
  if (Date.now() - cached.at > COMPOSER_FILE_REFERENCE_ROOT_TTL_MS) {
    rootCache.delete(key);
    return null;
  }
  return cached;
}

function toReferenceItems(entries: readonly FsEntry[]): ComposerReferenceItem[] {
  const items: ComposerReferenceItem[] = [];
  const seen = new Set<string>();
  for (const entry of entries) {
    // P0 只产出文件可插入项（目录插入语义未定，见方案 §4.3）；内部项纪律性再过滤一次。
    if (entry.type !== "file" || entry.internal === true) {
      continue;
    }
    const path = entry.path.trim();
    if (!path || seen.has(path)) {
      continue;
    }
    seen.add(path);
    items.push({
      id: path,
      label: entry.name || path,
      insertText: path,
      description: path,
    });
  }
  return items;
}

/** 远端模糊搜索结果 → 引用项（服务端已过滤/排序，这里只做路径去重与类型防御）。 */
function toSearchReferenceItems(items: readonly FsSearchItem[]): ComposerReferenceItem[] {
  const mapped: ComposerReferenceItem[] = [];
  const seen = new Set<string>();
  for (const item of items) {
    // `kinds=file` 已约束服务端；unknown/symlink 不伪装成可插入文件，再防御一次。
    if (item.type !== "file") {
      continue;
    }
    const path = item.path.trim();
    if (!path || seen.has(path)) {
      continue;
    }
    seen.add(path);
    mapped.push({
      id: path,
      label: item.name || path,
      insertText: path,
      description: path,
    });
  }
  return mapped;
}

export function useComposerFileReferences({
  sessionId,
  query,
  enabled,
  labels,
}: ComposerFileReferencesOptions): ComposerFileReferencesResult {
  const sessionKey = sessionId?.trim() ?? "";
  const [roots, setRoots] = useState<RootsState>({
    status: "loading",
    scope: null,
    error: null,
  });
  const [list, setList] = useState<ListState>(IDLE_LIST_STATE);
  const listSeqRef = useRef(0);
  // 已成功拉过首屏的作用域：搜索降级时复用它，不重复请求。
  const listingLoadedScopeRef = useRef<string | null>(null);
  // 端点能力是部署级属性：探测到不可用后在页面生命周期内粘住，避免每次输入都重复打一次 404。
  const [searchEndpointUnavailable, setSearchEndpointUnavailable] = useState(false);

  // 会话切换：作废在途请求与结果（作用域属于上一个会话时不得复用），不清除缓存。
  useEffect(() => {
    listSeqRef.current += 1;
    listingLoadedScopeRef.current = null;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 会话切换必须同步清空上一会话的结果并回到 loading 态（否则新会话会沿用旧会话候选/旧作用域根）；一次性收敛，不产生循环。
    setList(IDLE_LIST_STATE);
    setRoots({ status: "loading", scope: null, error: null });
  }, [sessionKey]);

  // 作用域解析：菜单打开（enabled）才请求；关闭时保留上次结果并停止请求。
  useEffect(() => {
    if (!enabled) {
      return;
    }
    const cached = readCachedRoot(sessionKey);
    if (cached) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 命中缓存时同步回填作用域，避免重开菜单闪 loading；一次性赋值。
      setRoots({
        status: cached.unavailable ? "unavailable" : "ready",
        scope: cached.root?.scope ?? null,
        error: null,
      });
      return;
    }

    const controller = new AbortController();
    let active = true;
    setRoots({ status: "loading", scope: null, error: null });

    fetchFsRoots({
      ...(sessionKey ? { sessionId: sessionKey } : {}),
      signal: controller.signal,
    })
      .then((result) => {
        if (!active) {
          return;
        }
        const root = pickDefaultRoot(result.roots.filter((item) => item.exists));
        rootCache.set(sessionKey, { root, unavailable: false, at: Date.now() });
        setRoots({ status: "ready", scope: root?.scope ?? null, error: null });
      })
      .catch((error) => {
        if (!active || isAbortError(error)) {
          return;
        }
        if (isFsRootsUnavailable(error)) {
          rootCache.set(sessionKey, { root: null, unavailable: true, at: Date.now() });
          setRoots({ status: "unavailable", scope: null, error });
          return;
        }
        // 网络类错误：保留当前作用域（可能是缓存值），仅置错误态供 UI 提示。
        setRoots((previous) => ({ ...previous, status: "error", error }));
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [enabled, sessionKey]);

  // P0：query 为空时拉首屏；P1 搜索端点不可用时也回退到该首屏做客户端过滤（§4.7.5）。
  const firstScreen = query.trim().length === 0;
  const scope = roots.scope;

  // P1：query 非空走远端模糊搜索；防抖/竞态/分页纪律全部由共享 useFsSearchQuery 承载。
  const search = useFsSearchQuery({
    scope: scope ?? "",
    query,
    // 菜单关闭 / 作用域未定 / 端点已知不可用：不发请求，但保留上次结果（重开菜单不闪空）。
    enabled: enabled && !firstScreen && scope !== null && !searchEndpointUnavailable,
    debounceMs: FS_SEARCH_DEBOUNCE_COMPOSER_MS,
    limit: COMPOSER_FILE_REFERENCE_LIMIT,
    kinds: "file",
    showHidden: false,
    keepResultsWhenDisabled: true,
  });

  useEffect(() => {
    if (search.unavailable) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 端点能力是部署级属性：一次探测到不可用即粘住，属于一次性收敛（同 use-reasoning-effort 的写法）。
      setSearchEndpointUnavailable(true);
    }
  }, [search.unavailable]);

  // 远端搜索是否真正接管渲染：query 非空且端点未被判定为不可用。
  const remoteSearch = !firstScreen && !searchEndpointUnavailable;
  useEffect(() => {
    if (!enabled || scope === null) {
      return;
    }
    // 空查询：照常拉首屏（含清空查询后的刷新）；搜索降级：仅当该作用域还没有首屏结果时补拉。
    if (!firstScreen && !(searchEndpointUnavailable && listingLoadedScopeRef.current !== scope)) {
      return;
    }
    const seq = listSeqRef.current + 1;
    listSeqRef.current = seq;
    const controller = new AbortController();

    // eslint-disable-next-line react-hooks/set-state-in-effect -- 发请求前打 loading 标记（保留上次结果），成功/失败在 .then/.catch 收敛，不会级联。
    setList((previous) => ({ ...previous, status: "loading", error: null }));

    fetchFsListing(
      {
        scope,
        path: "",
        limit: COMPOSER_FILE_REFERENCE_LIMIT,
        sort: "type_then_name",
        showHidden: false,
        dirsFirst: true,
      },
      { signal: controller.signal },
    )
      .then((result) => {
        if (listSeqRef.current !== seq) {
          return;
        }
        listingLoadedScopeRef.current = scope;
        setList({
          status: "ready",
          entries: result.entries,
          hasMore: result.hasMore,
          truncated: result.truncated,
          error: null,
        });
      })
      .catch((error) => {
        if (listSeqRef.current !== seq || isAbortError(error)) {
          return;
        }
        setList((previous) => ({ ...previous, status: "error", error }));
      });

    return () => {
      controller.abort();
    };
  }, [enabled, firstScreen, scope, searchEndpointUnavailable]);

  const group = useMemo<ComposerReferenceGroup | null>(() => {
    // 端点不可用 / 无可用根：不给分组，宿主继续用 artifacts 兜底。
    if (roots.status === "unavailable") {
      return null;
    }
    if (roots.status === "ready" && roots.scope === null) {
      return null;
    }

    const items = remoteSearch ? toSearchReferenceItems(search.items) : toReferenceItems(list.entries);
    const hasMore = remoteSearch ? search.hasMore : list.hasMore;
    const truncated = remoteSearch ? search.truncated : list.truncated;

    let status: ComposerReferenceStatus;
    if (remoteSearch) {
      // 作用域解析失败且没有可用 scope：搜索无从发起，直接给错误行。
      const rootsFailed = roots.status === "error" && roots.scope === null;
      if (rootsFailed || search.status === "error") {
        status = "error";
      } else if (search.status === "ready") {
        status = "ready";
      } else {
        // idle 只出现在「请求尚未起步」：打开态按 loading 展示，避免先闪「未找到匹配文件」。
        status = enabled || search.status === "loading" ? "loading" : "ready";
      }
    } else if (list.status === "loading") {
      status = "loading";
    } else if (list.status === "error" || roots.status === "error") {
      status = "error";
    } else if (list.status === "ready") {
      status = "ready";
    } else {
      // idle：根仍解析中，或（enabled=false 的）未开始加载。
      status = enabled && roots.status === "loading" ? "loading" : "ready";
    }

    // 错误且无历史结果：仍保留分组条目位，让菜单显示错误行而不是整组消失。
    const statusText =
      status === "loading" ? labels.loading : status === "error" ? labels.error : undefined;

    return {
      id: COMPOSER_FILE_REFERENCE_GROUP_ID,
      label: labels.group,
      items,
      status,
      // 远端结果已由服务端过滤/排序：跳过客户端 rankMatch 与重排（§4.5.2）。
      ...(remoteSearch ? { serverFiltered: true } : {}),
      ...(statusText ? { statusText } : {}),
      ...(items.length === 0 && status === "ready" ? { emptyText: labels.empty } : {}),
      hasMore,
      truncated,
      ...(truncated ? { truncatedText: labels.truncated } : {}),
    };
  }, [
    enabled,
    labels.empty,
    labels.error,
    labels.group,
    labels.loading,
    labels.truncated,
    list.entries,
    list.hasMore,
    list.status,
    list.truncated,
    remoteSearch,
    roots.scope,
    roots.status,
    search.hasMore,
    search.items,
    search.status,
    search.truncated,
  ]);

  return {
    group,
    status: remoteSearch ? search.status : list.status,
    hasMore: remoteSearch ? search.hasMore : list.hasMore,
    truncated: remoteSearch ? search.truncated : list.truncated,
    scope,
    error: roots.error ?? (remoteSearch ? search.error : list.error),
  };
}
