import { defineConfig } from "@playwright/test";

// e2e setup:
//  - mock-server.mjs serves the /api surface (scripted SSE chat streams) on
//    the port vite proxies /api to (8101 by default).
//  - `pnpm dev` serves the real app on 5193.
//  - Tests run against the system Chrome install (no browser download).

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
  use: {
    baseURL: "http://127.0.0.1:5193",
    channel: "chrome",
    headless: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: [
    {
      command: "node e2e/mock-server.mjs",
      env: { MOCK_PORT: "8111" },
      url: "http://127.0.0.1:8111/healthz",
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "pipe",
    },
    {
      command: "pnpm dev",
      env: { VITE_API_PROXY_PORT: "8111" },
      url: "http://127.0.0.1:5193",
      reuseExistingServer: false,
      timeout: 90_000,
      stdout: "pipe",
    },
  ],
});
