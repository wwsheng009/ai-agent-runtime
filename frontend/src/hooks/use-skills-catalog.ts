// P2-1B：工作台「技能」页签的只读目录加载收口。
//
// 与 `use-skills-market`（独立页面）共享同一份在途纪律，但只请求目录一条流：
//   * 一份在途：新请求 / 卸载 abort 前一个，序号丢弃过期响应；
//   * 主动 abort（卸载或刷新替换）不落错误态，只有真实失败进入 error；
//   * `count` 保留后端上报值，不按数组长度改写（与 skills 页面口径一致）；
//   * 状态由「请求轮次 + 结果归属」派生，effect 内不同步 setState（避免级联渲染）。

import { useCallback, useEffect, useRef, useState } from "react";

import { listRuntimeSkills } from "@/api/runtime/skills";
import type { RuntimeSkill } from "@/types/runtime";

export type SkillsCatalogStatus = "loading" | "ready" | "error";

export type UseSkillsCatalogResult = {
  skills: RuntimeSkill[];
  count: number;
  status: SkillsCatalogStatus;
  error: unknown;
  refresh: () => void;
};

type SkillsCatalogSnapshot = {
  key: number;
  skills: RuntimeSkill[];
  count: number;
};

type SkillsCatalogFailure = {
  key: number;
  error: unknown;
};

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

export function useSkillsCatalog(): UseSkillsCatalogResult {
  const [catalog, setCatalog] = useState<SkillsCatalogSnapshot | null>(null);
  const [failure, setFailure] = useState<SkillsCatalogFailure | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  const mountedRef = useRef(true);
  const seqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const seq = ++seqRef.current;
    const key = reloadKey;

    void listRuntimeSkills({ signal: controller.signal })
      .then((result) => {
        if (!mountedRef.current || seqRef.current !== seq) {
          return;
        }
        setCatalog({ key, skills: result.skills, count: result.count });
      })
      .catch((cause: unknown) => {
        if (!mountedRef.current || seqRef.current !== seq || isAbortError(cause)) {
          return;
        }
        setFailure({ key, error: cause });
      });

    return () => {
      controller.abort();
    };
  }, [reloadKey]);

  const failed = failure !== null && failure.key === reloadKey;
  const status: SkillsCatalogStatus = failed
    ? "error"
    : catalog !== null && catalog.key === reloadKey
      ? "ready"
      : "loading";

  const refresh = useCallback(() => {
    setReloadKey((key) => key + 1);
  }, []);

  return {
    skills: catalog?.skills ?? [],
    count: catalog?.count ?? 0,
    status,
    error: failed && failure !== null ? failure.error : null,
    refresh,
  };
}
