import { describe, expect, it } from "vitest";

import {
  buildRuntimeProxyRecord,
  getRuntimeProxyConfig,
  hasRuntimeProxyConfig,
  readRuntimeProxyConfig,
  summarizeRuntimeProxyConfig,
} from "./runtime-proxy-domain-utils";

describe("runtime-proxy-domain-utils", () => {
  it("reads providers.proxy from runtime config", () => {
    expect(
      getRuntimeProxyConfig({
        providers: {
          proxy: {
            enabled: true,
            http: "http://127.0.0.1:10810",
            https: "socks5://127.0.0.1:10811",
            no_proxy: "localhost,127.0.0.1",
          },
        },
      }),
    ).toEqual({
      enabled: true,
      http: "http://127.0.0.1:10810",
      https: "socks5://127.0.0.1:10811",
      noProxy: "localhost,127.0.0.1",
    });
  });

  it("builds proxy record and summary", () => {
    const config = readRuntimeProxyConfig({
      enabled: true,
      http: "http://127.0.0.1:10810",
      no_proxy: "localhost",
    });

    expect(hasRuntimeProxyConfig(config)).toBe(true);
    expect(summarizeRuntimeProxyConfig(config)).toBe("HTTP + NO_PROXY");
    expect(buildRuntimeProxyRecord(config)).toEqual({
      enabled: true,
      http: "http://127.0.0.1:10810",
      https: "",
      no_proxy: "localhost",
    });
  });

  it("treats empty config as env fallback", () => {
    const config = readRuntimeProxyConfig({});

    expect(hasRuntimeProxyConfig(config)).toBe(false);
    expect(summarizeRuntimeProxyConfig(config)).toBe("环境变量 / 直连");
  });
});
