import { useEffect, useRef, useState } from "react";

// P1-2：代码块视口懒激活。
// 高亮成本随代码量线性增长，长会话里多数代码块首屏并不可见。这里用文档级单例
// IntersectionObserver 复用观察集，元素首次相交即激活并永久移出观察集，
// 避免每个代码块各自创建观察器、也避免滚动反复触发。
type ActivationCallback = () => void;

let sharedObserver: IntersectionObserver | null = null;
let sharedObserverCtor: typeof IntersectionObserver | null = null;
const pendingActivations = new Map<Element, ActivationCallback>();

export function supportsViewportActivation() {
  return typeof IntersectionObserver === "function";
}

function getSharedObserver() {
  if (!supportsViewportActivation()) {
    return null;
  }

  // 观察器构造函数被替换时（测试替身切换/HMR/polyfill 升级）重建单例，
  // 否则会继续把元素交给已经失效的旧观察器。
  if (sharedObserver && sharedObserverCtor !== IntersectionObserver) {
    sharedObserver.disconnect();
    sharedObserver = null;
    pendingActivations.clear();
  }

  if (!sharedObserver) {
    sharedObserver = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) {
          continue;
        }

        const activate = pendingActivations.get(entry.target);
        if (!activate) {
          continue;
        }

        pendingActivations.delete(entry.target);
        // 激活后停止观察：一次性激活，滚动不再重复回调。
        sharedObserver?.unobserve(entry.target);
        activate();
      }
    });
    sharedObserverCtor = IntersectionObserver;
  }

  return sharedObserver;
}

function releaseActivation(target: Element) {
  if (!pendingActivations.delete(target)) {
    // 已经激活（回调内已移出观察集），无需重复 unobserve。
    return;
  }

  sharedObserver?.unobserve(target);
}

/**
 * 元素首次进入视口前保持 `activated === false`。
 *
 * - `enabled` 为 false 时（例如语言不支持高亮）不注册观察，直接报到激活态，
 *   由调用方按普通路径渲染。
 * - 环境不支持 IntersectionObserver 时（SSR/静态渲染/测试）同样直接激活，
 *   保证不依赖观察器也能得到完整输出。
 */
export function useViewportActivation<T extends Element>(enabled: boolean) {
  const [activated, setActivated] = useState(
    () => !enabled || !supportsViewportActivation(),
  );
  const targetRef = useRef<T | null>(null);

  useEffect(() => {
    const target = targetRef.current;
    if (activated || !enabled || !target) {
      return;
    }

    const observer = getSharedObserver();
    if (!observer) {
      return;
    }

    pendingActivations.set(target, () => setActivated(true));
    observer.observe(target);

    return () => {
      releaseActivation(target);
    };
  }, [activated, enabled]);

  return { activated, targetRef };
}
