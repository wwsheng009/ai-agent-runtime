import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

import { defineConfig } from "@playwright/test";

import {
  DEFAULT_LOCALE,
  DEFAULT_TIMEZONE,
  DEFAULT_VIEWPORT,
  requireDist,
} from "./e2e/support";

// e2e setup（P0-7）：
//  - 只跑构建产物：缺 `dist/index.html` 直接 fail-fast，提示先 `npm run build`；
//  - 端口全部由 OS 分配（mock + vite preview 各一个），不写死 5193/8111；
//  - 固定 locale / 时区 / 视口，避免日期与布局随机器漂移；
//  - 失败产物统一落 `.artifacts/`（gitignored）。
//  - mock-server 提供 /api 面；preview 通过 vite 的 preview.proxy 转发 /api。
//  - 用例统一从 `./fixtures` 导入 test/expect（失败时额外补全页截图）。

// Playwright 只接受「单一对象」配置（不支持 async 工厂）：端口探测放到
// `e2e/probe-ports.mjs` 子进程里同步取回。
//
// 注意：worker 进程会**再次求值本文件**（配置里包含 test.use 等），若不回写
// 环境变量，worker 会重新探测出另一组端口，baseURL 就指向没有服务的端口
// （ECONNREFUSED）。因此解析结果必须写回 E2E_MOCK_PORT / E2E_PREVIEW_PORT，
// 由 runner 派生的 worker 继承；显式设置这两个变量可复现固定端口。
function isValidPort(value: number): boolean {
  return Number.isInteger(value) && value > 0 && value < 65536;
}

function resolvePorts(): { mockPort: number; previewPort: number } {
  const envMock = Number(process.env.E2E_MOCK_PORT);
  const envPreview = Number(process.env.E2E_PREVIEW_PORT);
  if (isValidPort(envMock) && isValidPort(envPreview)) {
    return { mockPort: envMock, previewPort: envPreview };
  }

  const script = fileURLToPath(new URL("./e2e/probe-ports.mjs", import.meta.url));
  const raw = execFileSync(process.execPath, [script], { encoding: "utf8" }).trim();
  const probed = JSON.parse(raw) as { mockPort: number; previewPort: number };
  return {
    mockPort: isValidPort(envMock) ? envMock : probed.mockPort,
    previewPort: isValidPort(envPreview) ? envPreview : probed.previewPort,
  };
}

requireDist();

const { mockPort, previewPort } = resolvePorts();
const baseURL = `http://127.0.0.1:${previewPort}`;
// global-setup 与用例共用同一 baseURL（runner 进程内可见）。
process.env.E2E_BASE_URL = baseURL;
process.env.E2E_MOCK_PORT = String(mockPort);
process.env.E2E_PREVIEW_PORT = String(previewPort);

export default defineConfig({
    testDir: "./e2e",
    globalSetup: "./e2e/global-setup.ts",
    timeout: 60_000,
    expect: {
      timeout: 15_000,
    },
    fullyParallel: false,
    workers: 1,
    retries: 0,
    reporter: [["list"]],
    outputDir: ".artifacts/playwright",
    use: {
      baseURL,
      channel: "chrome",
      headless: true,
      trace: "retain-on-failure",
      screenshot: "only-on-failure",
      locale: DEFAULT_LOCALE,
      timezoneId: DEFAULT_TIMEZONE,
      viewport: DEFAULT_VIEWPORT,
    },
    webServer: [
      {
        command: "node e2e/mock-server.mjs",
        env: { MOCK_PORT: String(mockPort) },
        url: `http://127.0.0.1:${mockPort}/healthz`,
        reuseExistingServer: false,
        timeout: 30_000,
        stdout: "pipe",
      },
      {
        command: `npx vite preview --host 127.0.0.1 --port ${previewPort} --strictPort`,
        env: { VITE_API_PROXY_PORT: String(mockPort), VITE_DEV_HOST: "127.0.0.1" },
        url: `${baseURL}/`,
        reuseExistingServer: false,
        timeout: 60_000,
        stdout: "pipe",
      },
    ],
});
