// P2-7 / Batch 12 共享测试脚手架：命令执行器的两个测试文件（基础命令、
// `/profile` 执行分支）共用同一份渲染生命周期与控制器快照逻辑——行数门禁
// （verify-max-lines）要求拆分时，拆出的两份不应各自复制一套 render/flush
// 后各自漂移。
//
// 注意：`vi.mock` / `vi.hoisted` 留在各测试文件内。mock 是按**测试文件**提升的，
// 放进共享模块会被当成普通导入，拦截不生效。
//
// `notice` 是渲染快照的一部分：断言一律读最近一次渲染的控制器（`current()`），
// 而不是发起命令时那个旧引用。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import {
  useComposerCommandExecutor,
  type ComposerCommandExecutor,
  type UseComposerCommandExecutorOptions,
} from "./use-composer-command-executor";
import type { ComposerCommand } from "@/lib/composer-commands";

export type ComposerExecutorHarness = {
  /** 创建挂载点与 root（beforeEach 用）。 */
  mount(): void;
  /** 卸载并移除挂载点（afterEach 用；未挂载时是空操作）。 */
  unmount(): void;
  /** 渲染一次并返回最近一次渲染的控制器快照。 */
  render(
    options: UseComposerCommandExecutorOptions,
  ): Promise<ComposerCommandExecutor>;
  /** 最近一次渲染的控制器快照（notice 随之更新）。 */
  current(): ComposerCommandExecutor;
  /** 等状态回填（两次微任务，覆盖处理器里的 await 链）。 */
  flush(): Promise<void>;
  /** 派发一条命令并等状态回填，返回宿主是否认领。 */
  run(command: ComposerCommand, args: string): Promise<boolean>;
};

// 测试脚手架模块，不参与 HMR fast refresh：本文件导出的是工厂函数（非组件），
// 宿主组件只在本模块内部使用、不外传。
// eslint-disable-next-line react-refresh/only-export-components
function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (executor: ComposerCommandExecutor) => void;
  options: UseComposerCommandExecutorOptions;
}) {
  const executor = useComposerCommandExecutor(options);
  onSnapshot(executor);
  return null;
}

export function createComposerExecutorHarness(): ComposerExecutorHarness {
  let container: HTMLDivElement | null = null;
  let root: Root | null = null;
  let latest: ComposerCommandExecutor | null = null;

  function current(): ComposerCommandExecutor {
    if (!latest) {
      throw new Error("test setup: harness has not rendered yet");
    }
    return latest;
  }

  async function flush(): Promise<void> {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  async function render(
    options: UseComposerCommandExecutorOptions,
  ): Promise<ComposerCommandExecutor> {
    const target = root;
    if (!target) {
      throw new Error("test setup: harness is not mounted");
    }
    await act(async () => {
      target.render(
        <Harness onSnapshot={(value) => (latest = value)} options={options} />,
      );
    });
    return current();
  }

  async function run(command: ComposerCommand, args: string): Promise<boolean> {
    let handled = false;
    await act(async () => {
      handled = current().run(command, args);
    });
    await flush();
    return handled;
  }

  function mount(): void {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
  }

  function unmount(): void {
    const target = root;
    if (target) {
      act(() => target.unmount());
    }
    container?.remove();
    root = null;
    container = null;
    latest = null;
  }

  return { mount, unmount, render, current, flush, run };
}
