// P0-7：为 playwright.config.ts 同步申请空闲端口。
//
// playwight 配置必须是「单一对象」，不支持 async 工厂，因此由 node 子进程
// 探测后把结果以 JSON 打到 stdout，配置文件用 execFileSync 读取。
// `e2e/support.ts` 导出的异步 `probeFreePort` 供用例/脚本在运行期使用，
// 两者实现同一语义（bind 0 → 读端口 → 关闭 → 复用），此处不引入 TS 依赖。

import { createServer } from "node:net";

function probeFreePort() {
  return new Promise((resolvePort, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      if (address === null || typeof address === "string") {
        probe.close(() => reject(new Error("port probe returned no address")));
        return;
      }
      probe.close(() => resolvePort(address.port));
    });
  });
}

const [mockPort, previewPort] = await Promise.all([probeFreePort(), probeFreePort()]);
process.stdout.write(JSON.stringify({ mockPort, previewPort }));
