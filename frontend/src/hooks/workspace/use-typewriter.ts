import { useEffect, useRef, useState } from "react";

/**
 * useTypewriter — 打字机效果 hook。
 *
 * 流式响应期间（active=true）把 content 按字符渐进显示：
 * 每个 tick 只暴露到 shown 个字符，形成"逐字打出"的视觉效果；
 * 网络到达快于打字速度时自动追赶（pending 超过阈值会加速），
 * 到达慢时则紧跟真实流式节奏；流式暂停时继续打到当前全长。
 *
 * active=false（历史消息 / 已完成 / 非流式）时不做任何截断，
 * 直接返回完整 content，零额外开销。
 *
 * 实现要点：
 * - content 用两路同步：latestContent（render 期间官方
 *   "storing information from previous renders" 模式，用于返回值）
 *   与 contentRef（effect 内同步，供 interval 回调读取最新值，
 *   避免每个 chunk 都重建 interval）。
 * - 推进步进在代理对边界处回退，避免把 emoji 等 astral 字符
 *   切成两个孤立 code unit（渲染半个字符闪烁）。
 * - active 从 false → true（新一轮流式）时重置进度，从头打字。
 */

const TICK_MS = 30; // 渲染 tick 间隔
const BASE_CPS = 55; // 基础打字速度（字符/秒）
const FAST_CPS = 180; // 追赶加速（字符/秒）
const FAST_PENDING_THRESHOLD = 2000; // pending 超过该字符数进入加速模式

export function useTypewriter(content: string, active: boolean): string {
  // render 期间同步最新 content（替代 render 内写 ref）。
  const [latestContent, setLatestContent] = useState(content);
  if (latestContent !== content) {
    setLatestContent(content);
  }

  // interval 回调读取的最新 content（effect 内同步，渲染期不碰 ref）。
  const contentRef = useRef(content);
  useEffect(() => {
    contentRef.current = content;
  });

  const [shown, setShown] = useState(0);

  // active 从 false -> true 时重置打字进度（新一轮流式从头开始）。
  const [prevActive, setPrevActive] = useState(active);
  if (prevActive !== active) {
    setPrevActive(active);
    if (active) {
      setShown(0);
    }
  }

  // 打字机 interval 只随 active 起停：进度推进全部走函数式更新，
  // content 的最新值经 contentRef 读取，避免陈旧闭包与频繁重建。
  useEffect(() => {
    if (!active) {
      return;
    }
    const intervalId = window.setInterval(() => {
      setShown((prev) => {
        const text = contentRef.current;
        const targetLen = text.length;
        const pending = targetLen - prev;
        if (pending <= 0) {
          return prev; // 已追上当前进度，等待新内容
        }
        const cps =
          pending > FAST_PENDING_THRESHOLD ? FAST_CPS : BASE_CPS;
        const step = Math.max(1, Math.round((cps * TICK_MS) / 1000));
        let next = Math.min(targetLen, prev + step);
        // 若落点恰在代理对中间（low surrogate），回退一个 code unit，
        // 保证暴露的前缀始终是完整的字符序列。
        if (next > 0 && next < targetLen) {
          const code = text.charCodeAt(next);
          if (code >= 0xdc00 && code <= 0xdfff) {
            next -= 1;
          }
        }
        return next;
      });
    }, TICK_MS);
    return () => window.clearInterval(intervalId);
  }, [active]);

  if (!active) {
    return latestContent;
  }
  return latestContent.slice(0, shown);
}