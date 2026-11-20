// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";

import { listRuntimeProviderSummaries } from "../runtime-provider-config-utils";
import { ProviderTable } from "./provider-table";

describe("ProviderTable", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT;
  });

  it("keeps actions reachable, compacts status details, and exposes secondary actions in a menu", async () => {
    const onCopyProvider = vi.fn();
    const onCreateProvider = vi.fn();
    const onImportProvider = vi.fn();
    const onDeleteProvider = vi.fn();
    const onEditProvider = vi.fn();
    const onRefreshProviderAccount = vi.fn();
    const onSetDefaultProvider = vi.fn();
    const providers = listRuntimeProviderSummaries({
      providers: {
        items: {
          alpha: {
            enabled: true,
            protocol: "openai",
            default_model: "model-a",
            site_type: "sub2api",
            site_type_confidence: "medium",
            account: {
              balance_label: "remaining (USD)",
              balance_text: "-1 USD (unrestricted, source=v1_usage)",
              fetched_at: "2026-10-19T09:21:51Z",
            },
            custom_field: true,
          },
          beta: { enabled: false, protocol: "anthropic" },
        },
      },
    });

    await act(async () => {
      root?.render(
        <ProviderTable
          copiedProviderName={null}
          defaultProvider="alpha"
          enabledCount={1}
          onCopyProvider={onCopyProvider}
          onCreateProvider={onCreateProvider}
          onImportProvider={onImportProvider}
          onDeleteProvider={onDeleteProvider}
          onEditProvider={onEditProvider}
          onRefreshProviderAccount={onRefreshProviderAccount}
          onSetDefaultProvider={onSetDefaultProvider}
          providers={providers}
          rowBusyName={null}
        />,
      );
    });

    expect(container.querySelector(".overflow-x-auto")).not.toBeNull();
    expect(
      container.querySelector('th[aria-sort="none"]'),
    ).not.toBeNull();
    expect(container.querySelector("th.sticky.right-0")).not.toBeNull();
    expect(container.querySelector("td.sticky.right-0")).not.toBeNull();
    expect(container.textContent).toContain("+1");
    expect(container.textContent).not.toContain("1 个扩展字段");

    const betaRow = Array.from(container.querySelectorAll("tbody tr")).find(
      (row) => row.textContent?.includes("beta"),
    );
    const menuButton = betaRow?.querySelector<HTMLButtonElement>(
      'button[aria-haspopup="menu"]',
    );
    expect(menuButton).toBeInstanceOf(HTMLButtonElement);
    await act(async () => {
      menuButton?.click();
    });

    const menu = document.body.querySelector('[role="menu"]');
    expect(menu).not.toBeNull();
    const syncAction = Array.from(
      menu?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [],
    ).find((button) => button.textContent?.includes("同步"));
    await act(async () => {
      syncAction?.click();
    });
    expect(onRefreshProviderAccount).toHaveBeenCalledWith("beta");

    const t = i18n.getFixedT(null, "runtimeConfig") as unknown as (
      key: string,
      options?: Record<string, unknown>,
    ) => string;
    expect(menuButton?.getAttribute("aria-label")).toBe(
      t("editor.providers.actions.openMenu", { name: "beta" }),
    );
  });
});
