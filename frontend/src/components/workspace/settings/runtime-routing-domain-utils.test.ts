import { describe, expect, it } from "vitest";

import {
  createDefaultRuntimeRoute,
  getRuntimeRoutingConfig,
  listRuntimeRouteSummaries,
} from "./runtime-routing-domain-utils";

describe("runtime-routing-domain-utils", () => {
  it("reads routing root config", () => {
    expect(
      getRuntimeRoutingConfig({
        routing: {
          strategy: "health",
          failover: true,
        },
      }),
    ).toEqual({
      strategy: "health",
      failover: true,
    });
  });

  it("lists route summaries from routing.routes", () => {
    const routes = listRuntimeRouteSummaries({
      routing: {
        routes: [
          {
            match_path: "/v1/chat",
            match_type: "prefix",
            group: "openai_group",
            pipeline: "chat-completions",
            match_models: ["gpt-*"],
            exclude_models: ["gpt-5.4"],
            priority: 10,
            custom_flag: true,
          },
        ],
      },
    });

    expect(routes).toHaveLength(1);
    expect(routes[0]).toMatchObject({
      index: 0,
      matchPath: "/v1/chat",
      matchType: "prefix",
      group: "openai_group",
      matchModels: ["gpt-*"],
      excludeModels: ["gpt-5.4"],
      priority: "10",
      extraFieldCount: 1,
    });
  });

  it("creates a sensible default route draft source", () => {
    expect(createDefaultRuntimeRoute()).toMatchObject({
      match_path: "/v1/chat",
      match_type: "prefix",
      group: "",
    });
  });
});
