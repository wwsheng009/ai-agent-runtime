import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { ConfigDomainTable } from "./config-domain-table";

type Row = {
  id: string;
  name: string;
  tags: string[];
};

const rows: Row[] = Array.from({ length: 23 }, (_, index) => ({
  id: `p${index + 1}`,
  name: `provider-${String(index + 1).padStart(2, "0")}`,
  tags: index % 3 === 0 ? ["openai", "fast"] : ["azure"],
}));

function getRowKey(row: Row) {
  return row.id;
}

function searchText(row: Row) {
  return [row.name, ...row.tags].join(" ");
}

function renderTable(
  props: Partial<React.ComponentProps<typeof ConfigDomainTable<Row>>> = {},
) {
  return (
    <ConfigDomainTable<Row>
      title="Providers"
      items={rows}
      getRowKey={getRowKey}
      emptyState="no providers yet"
      columns={[
        { header: "Name", cell: (row) => row.name },
        { header: "Tags", cell: (row) => row.tags.join(", ") },
      ]}
      {...props}
    />
  );
}

describe("ConfigDomainTable", () => {
  it("renders every row when neither search nor pagination is enabled", () => {
    const markup = renderToStaticMarkup(renderTable());
    expect(markup).toContain("provider-01");
    expect(markup).toContain("provider-23");
  });

  it("filters rows by search text and falls back to the empty state on no match", async () => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    const actEnvironment = globalThis as typeof globalThis & {
      IS_REACT_ACT_ENVIRONMENT?: boolean;
    };
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = true;

    await act(async () => {
      root.render(
        renderTable({
          searchable: true,
          getSearchText: searchText,
          pageSize: 10,
        }),
      );
    });

    const input = container.querySelector(
      "input[type='search']",
    ) as HTMLInputElement | null;
    expect(input).toBeInstanceOf(HTMLInputElement);

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )?.set;
      setter?.call(input, "provider-05");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });

    const text = container.textContent ?? "";
    expect(text).toContain("provider-05");
    expect(text).not.toContain("provider-06");
    expect(text).not.toContain("provider-01");

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )?.set;
      setter?.call(input, "does-not-exist");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });

    expect(container.textContent ?? "").toContain("没有匹配的条目");
  });

  it("paginates rows with a page size and navigates between pages", async () => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    const actEnvironment = globalThis as typeof globalThis & {
      IS_REACT_ACT_ENVIRONMENT?: boolean;
    };
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = true;

    await act(async () => {
      root.render(renderTable({ pageSize: 10 }));
    });

    const text = container.textContent ?? "";
    expect(text).toContain("provider-01");
    expect(text).toContain("provider-10");
    expect(text).not.toContain("provider-11");
    expect(text).toContain("第 1-10 条，共 23 条");

    const buttons = Array.from(container.querySelectorAll("button"));
    const nextButton = buttons.find((button) =>
      button.getAttribute("aria-label")?.includes("下一页"),
    );
    expect(nextButton).toBeInstanceOf(HTMLButtonElement);

    await act(async () => {
      nextButton?.click();
    });

    const afterNext = container.textContent ?? "";
    expect(afterNext).toContain("provider-11");
    expect(afterNext).not.toContain("provider-01");
    expect(afterNext).toContain("第 11-20 条，共 23 条");
    expect(afterNext).toContain("2 / 3");

    await act(async () => {
      nextButton?.click();
    });

    const afterSecondNext = container.textContent ?? "";
    expect(afterSecondNext).toContain("provider-21");
    expect(afterSecondNext).toContain("第 21-23 条，共 23 条");
    expect(afterSecondNext).toContain("3 / 3");
  });
});