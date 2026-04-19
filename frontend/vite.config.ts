import path from "node:path";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { HmrOptions, ProxyOptions } from "vite";
import { defineConfig, loadEnv } from "vite";

function readString(
  env: Record<string, string>,
  key: string,
): string | undefined {
  const value = env[key]?.trim();
  return value ? value : undefined;
}

function readNumber(
  env: Record<string, string>,
  key: string,
  fallback: number,
): number {
  const value = readString(env, key);
  if (!value) {
    return fallback;
  }

  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function readOptionalNumber(
  env: Record<string, string>,
  key: string,
): number | undefined {
  const value = readString(env, key);
  if (!value) {
    return undefined;
  }

  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

function readList(env: Record<string, string>, key: string): string[] {
  const value = readString(env, key);
  if (!value) {
    return [];
  }

  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function dedupe(values: string[]) {
  return [...new Set(values)];
}

function createProxy(target: string): ProxyOptions {
  return {
    target,
    changeOrigin: true,
    secure: false,
  };
}

function resolveManualChunk(id: string) {
  const normalizedId = id.replace(/\\/g, "/");

  if (normalizedId.includes("/node_modules/react-dom/")) {
    return "vendor-react";
  }

  if (
    normalizedId.includes("/node_modules/react/") ||
    normalizedId.includes("/node_modules/scheduler/")
  ) {
    return "vendor-react";
  }

  if (
    normalizedId.includes("/node_modules/react-router/") ||
    normalizedId.includes("/node_modules/react-router-dom/")
  ) {
    return "vendor-router";
  }

  if (
    normalizedId.includes("/node_modules/lucide-react/") ||
    normalizedId.includes("/node_modules/clsx/") ||
    normalizedId.includes("/node_modules/class-variance-authority/") ||
    normalizedId.includes("/node_modules/tailwind-merge/")
  ) {
    return "vendor-ui";
  }

  return undefined;
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const devHost = readString(env, "VITE_DEV_HOST") ?? "0.0.0.0";
  const devPort = readNumber(env, "VITE_DEV_PORT", 5193);
  const backendPort = readNumber(env, "VITE_API_PROXY_PORT", 8101);
  const proxyTarget =
    readString(env, "VITE_API_PROXY_TARGET") ??
    `http://127.0.0.1:${backendPort}`;
  const publicOrigin = readString(env, "VITE_DEV_PUBLIC_ORIGIN");
  const publicUrl = publicOrigin ? new URL(publicOrigin) : undefined;
  const allowedHosts = dedupe([
    ...readList(env, "VITE_DEV_ALLOWED_HOSTS"),
    ...(publicUrl?.hostname ? [publicUrl.hostname] : []),
  ]);

  const hmr: HmrOptions | undefined =
    publicUrl ||
    readString(env, "VITE_DEV_HMR_HOST") ||
    readString(env, "VITE_DEV_HMR_PROTOCOL") ||
    readOptionalNumber(env, "VITE_DEV_HMR_PORT") ||
    readOptionalNumber(env, "VITE_DEV_HMR_CLIENT_PORT") ||
    readString(env, "VITE_DEV_HMR_PATH")
      ? {
          host:
            readString(env, "VITE_DEV_HMR_HOST") ?? publicUrl?.hostname,
          protocol:
            readString(env, "VITE_DEV_HMR_PROTOCOL") ??
            publicUrl?.protocol.replace(":", ""),
          port:
            readOptionalNumber(env, "VITE_DEV_HMR_PORT") ??
            (publicUrl?.port ? Number(publicUrl.port) : undefined),
          clientPort:
            readOptionalNumber(env, "VITE_DEV_HMR_CLIENT_PORT") ??
            (publicUrl?.port ? Number(publicUrl.port) : undefined),
          path: readString(env, "VITE_DEV_HMR_PATH"),
        }
      : undefined;

  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    server: {
      host: devHost,
      port: devPort,
      strictPort: true,
      allowedHosts: allowedHosts.length > 0 ? allowedHosts : undefined,
      origin: publicOrigin,
      hmr,
      proxy: {
        "/api": createProxy(proxyTarget),
        "/healthz": createProxy(proxyTarget),
      },
    },
    build: {
      rolldownOptions: {
        output: {
          manualChunks: resolveManualChunk,
        },
      },
    },
  };
});
