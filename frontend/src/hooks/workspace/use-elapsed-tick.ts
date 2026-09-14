// P2-1A：秒级 elapsed 走时（仅在有 live 任务时启动定时器，避免空转渲染）。
// 返回当前时间戳（ms），由调用方用 resolveJobElapsedMs(job, now) 求差。

import { useEffect, useState } from "react";

export function useElapsedTick(active: boolean, intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!active) {
      return;
    }
    // 激活时异步对齐一次，避免暂停期间的时间差让首帧显示旧值；
    // 不在 effect 体内同步 setState（react-hooks/set-state-in-effect）。
    const kickoff = setTimeout(() => {
      setNow(Date.now());
    }, 0);
    const timer = setInterval(() => {
      setNow(Date.now());
    }, intervalMs);
    return () => {
      clearTimeout(kickoff);
      clearInterval(timer);
    };
  }, [active, intervalMs]);

  return now;
}
