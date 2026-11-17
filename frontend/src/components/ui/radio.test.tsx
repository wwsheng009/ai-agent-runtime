import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { RadioInput } from "./radio";

describe("RadioInput", () => {
  it("uses the shared choice size and tone configuration", () => {
    const markup = renderToStaticMarkup(
      <RadioInput aria-label="mode" size="sm" tone="teal" />,
    );

    expect(markup).toContain('type="radio"');
    expect(markup).toContain("size-3.5");
    expect(markup).toContain("accent-accent-teal");
  });
});