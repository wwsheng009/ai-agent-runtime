// P2-1A：技能市场 / 热重载面板状态机。
//
// 设计要点（对齐既有 use-usage-quota / use-file-preview 的收口纪律）：
//   * 目录、统计、热重载三条流各自独立请求、独立错误——任一条失败不遮蔽其余；
//   * 检索与目录是「一份在途」语义：新请求 / 卸载都 abort 前一个，序号丢弃过期响应；
//   * 主动 abort（组件卸载或新请求替换）不落错误态，只有真实失败才进入 error；
//   * 写操作（热重载 start/stop/reload）完成即用响应里的 stats 覆盖本地快照，
//     失败原样保留错误交给 UI 分类（403 策略拒绝 / 503 未配置 / 其他失败）。

import { useCallback, useEffect, useRef, useState } from "react";

import {
  getHotReloadStats,
  getRuntimeSkillsStats,
  listRuntimeSkills,
  reloadHotReload,
  searchRuntimeSkills,
  startHotReload,
  stopHotReload,
  type RuntimeSkillSearchModeInput,
} from "@/api/runtime/skills";
import { readAdminToken } from "@/lib/admin-token";
import type {
  RuntimeHotReloadStats,
  RuntimeSkill,
  RuntimeSkillSearchResult,
  RuntimeSkillStats,
} from "@/types/runtime";

export type SkillsStreamStatus = "loading" | "ready" | "error";
export type SkillsSearchStatus = "idle" | "loading" | "ready" | "error";
export type HotReloadAction = "start" | "stop" | "reload";

export type SkillSearchInput = {
  query: string;
  category?: string;
  mode?: RuntimeSkillSearchModeInput;
};

export type UseSkillsMarketOptions = {
  /** 来源层级 / 目录过滤（透传后端 `source_layer` / `source_dir`）。 */
  layer?: string;
  dir?: string;
};

export type UseSkillsMarketResult = {
  catalog: RuntimeSkill[];
  catalogCount: number;
  catalogStatus: SkillsStreamStatus;
  catalogError: unknown;
  refreshCatalog: () => void;

  searchQuery: string;
  searchCategory: string;
  searchMode: RuntimeSkillSearchModeInput;
  searchStatus: SkillsSearchStatus;
  searchResult: RuntimeSkillSearchResult | null;
  searchError: unknown;
  runSearch: (input: SkillSearchInput) => void;
  clearSearch: () => void;

  stats: RuntimeSkillStats | null;
  statsStatus: SkillsStreamStatus;
  statsError: unknown;
  refreshStats: () => void;

  hotReload: RuntimeHotReloadStats | null;
  hotReloadStatus: SkillsStreamStatus;
  hotReloadError: unknown;
  refreshHotReload: () => void;
  hotReloadAction: HotReloadAction | null;
  hotReloadActionError: unknown;
  startHotReloadDirs: (dirs: string[], debounceMs?: number) => Promise<void>;
  stopHotReloading: () => Promise<void>;
  reloadHotReloadNow: () => Promise<void>;
};

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

export function useSkillsMarket(
  options: UseSkillsMarketOptions = {},
  ): UseSkillsMarketResult {
  const { layer, dir } = options;

  const [catalog, setCatalog] = useState<RuntimeSkill[]>([]);
  const [catalogCount, setCatalogCount] = useState(0);
  const [catalogStatus, setCatalogStatus] = useState<SkillsStreamStatus>("loading");
  const [catalogError, setCatalogError] = useState<unknown>(null);
  const [catalogReloadKey, setCatalogReloadKey] = useState(0);

  const [searchStatus, setSearchStatus] = useState<SkillsSearchStatus>("idle");
  const [searchResult, setSearchResult] = useState<RuntimeSkillSearchResult | null>(null);
  const [searchError, setSearchError] = useState<unknown>(null);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchCategory, setSearchCategory] = useState("");
  const [searchMode, setSearchMode] = useState<RuntimeSkillSearchModeInput>("auto");

  const [stats, setStats] = useState<RuntimeSkillStats | null>(null);
  const [statsStatus, setStatsStatus] = useState<SkillsStreamStatus>("loading");
  const [statsError, setStatsError] = useState<unknown>(null);
  const [statsReloadKey, setStatsReloadKey] = useState(0);

  const [hotReload, setHotReload] = useState<RuntimeHotReloadStats | null>(null);
  const [hotReloadStatus, setHotReloadStatus] = useState<SkillsStreamStatus>("loading");
  const [hotReloadError, setHotReloadError] = useState<unknown>(null);
  const [hotReloadReloadKey, setHotReloadReloadKey] = useState(0);
  const [hotReloadAction, setHotReloadAction] = useState<HotReloadAction | null>(null);
  const [hotReloadActionError, setHotReloadActionError] = useState<unknown>(null);

  const mountedRef = useRef(true);
  const catalogSeqRef = useRef(0);
  const searchSeqRef = useRef(0);
  const statsSeqRef = useRef(0);
  const hotReloadSeqRef = useRef(0);
  const searchAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      searchAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const seq = ++catalogSeqRef.current;
    setCatalogStatus("loading");
    setCatalogError(null);

    void listRuntimeSkills({ layer, dir, signal: controller.signal })
      .then((result) => {
        if (!mountedRef.current || catalogSeqRef.current !== seq) {
          return;
        }
        setCatalog(result.skills);
        setCatalogCount(result.count);
        setCatalogStatus("ready");
      })
      .catch((error: unknown) => {
        if (!mountedRef.current || catalogSeqRef.current !== seq || isAbortError(error)) {
          return;
        }
        setCatalogStatus("error");
        setCatalogError(error);
      });

    return () => {
      controller.abort();
    };
  }, [layer, dir, catalogReloadKey]);

  useEffect(() => {
    const controller = new AbortController();
    const seq = ++statsSeqRef.current;
    setStatsStatus("loading");
    setStatsError(null);

    void getRuntimeSkillsStats({ layer, dir, signal: controller.signal })
      .then((result) => {
        if (!mountedRef.current || statsSeqRef.current !== seq) {
          return;
        }
        setStats(result);
        setStatsStatus("ready");
      })
      .catch((error: unknown) => {
        if (!mountedRef.current || statsSeqRef.current !== seq || isAbortError(error)) {
          return;
        }
        setStatsStatus("error");
        setStatsError(error);
      });

    return () => {
      controller.abort();
    };
  }, [layer, dir, statsReloadKey]);

  useEffect(() => {
    const controller = new AbortController();
    const seq = ++hotReloadSeqRef.current;
    setHotReloadStatus("loading");
    setHotReloadError(null);

    void getHotReloadStats({ signal: controller.signal })
      .then((result) => {
        if (!mountedRef.current || hotReloadSeqRef.current !== seq) {
          return;
        }
        setHotReload(result);
        setHotReloadStatus("ready");
      })
      .catch((error: unknown) => {
        if (!mountedRef.current || hotReloadSeqRef.current !== seq || isAbortError(error)) {
          return;
        }
        setHotReloadStatus("error");
        setHotReloadError(error);
      });

    return () => {
      controller.abort();
    };
  }, [hotReloadReloadKey]);

  const refreshCatalog = useCallback(() => {
    setCatalogReloadKey((key) => key + 1);
  }, []);

  const refreshStats = useCallback(() => {
    setStatsReloadKey((key) => key + 1);
  }, []);

  const refreshHotReload = useCallback(() => {
    setHotReloadReloadKey((key) => key + 1);
  }, []);

  const runSearch = useCallback(
    (input: SkillSearchInput) => {
      const query = input.query.trim();
      if (!query) {
        return;
      }

      searchAbortRef.current?.abort();
      const controller = new AbortController();
      searchAbortRef.current = controller;
      const seq = ++searchSeqRef.current;

      setSearchQuery(query);
      setSearchCategory(input.category?.trim() ?? "");
      setSearchMode(input.mode ?? "auto");
      setSearchStatus("loading");
      setSearchError(null);

      void searchRuntimeSkills(
        {
          query,
          category: input.category,
          mode: input.mode,
        },
        { layer, dir, signal: controller.signal },
      )
        .then((result) => {
          if (!mountedRef.current || searchSeqRef.current !== seq) {
            return;
          }
          setSearchResult(result);
          setSearchStatus("ready");
        })
        .catch((error: unknown) => {
          if (!mountedRef.current || searchSeqRef.current !== seq || isAbortError(error)) {
            return;
          }
          setSearchStatus("error");
          setSearchError(error);
        });
    },
    [layer, dir],
  );

  const clearSearch = useCallback(() => {
    searchAbortRef.current?.abort();
    searchSeqRef.current += 1;
    setSearchStatus("idle");
    setSearchResult(null);
    setSearchError(null);
    setSearchQuery("");
    setSearchCategory("");
    setSearchMode("auto");
  }, []);

  const runHotReloadAction = useCallback(
    async (action: "stop" | "reload") => {
      setHotReloadAction(action);
      setHotReloadActionError(null);
      try {
        const adminToken = readAdminToken();
        const next =
          action === "stop"
            ? await stopHotReload({ adminToken })
            : await reloadHotReload({ adminToken });
        if (!mountedRef.current) {
          return;
        }
        setHotReload(next);
        setHotReloadStatus("ready");
        setHotReloadError(null);
      } catch (error: unknown) {
        if (!mountedRef.current || isAbortError(error)) {
          return;
        }
        setHotReloadActionError(error);
      } finally {
        if (mountedRef.current) {
          setHotReloadAction(null);
        }
      }
    },
    [],
  );

  const startHotReloadDirs = useCallback(
    (dirs: string[], debounceMs?: number) => {
      setHotReloadAction("start");
      setHotReloadActionError(null);
      return startHotReload(dirs, { adminToken: readAdminToken(), debounceMs })
        .then((result) => {
          if (!mountedRef.current) {
            return;
          }
          setHotReload(result);
          setHotReloadStatus("ready");
          setHotReloadError(null);
        })
        .catch((error: unknown) => {
          if (!mountedRef.current) {
            return;
          }
          setHotReloadActionError(error);
        })
        .finally(() => {
          if (mountedRef.current) {
            setHotReloadAction(null);
          }
        });
    },
    [setHotReloadAction, setHotReloadActionError],
  );

  const stopHotReloading = useCallback(
    () => runHotReloadAction("stop"),
    [runHotReloadAction],
  );

  const reloadHotReloadNow = useCallback(
    () => runHotReloadAction("reload"),
    [runHotReloadAction],
  );

  return {
    catalog,
    catalogCount,
    catalogStatus,
    catalogError,
    refreshCatalog,
    searchQuery,
    searchCategory,
    searchMode,
    searchStatus,
    searchResult,
    searchError,
    runSearch,
    clearSearch,
    stats,
    statsStatus,
    statsError,
    refreshStats,
    hotReload,
    hotReloadStatus,
    hotReloadError,
    refreshHotReload,
    hotReloadAction,
    hotReloadActionError,
    startHotReloadDirs,
    stopHotReloading,
    reloadHotReloadNow,
  };
}
