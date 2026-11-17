import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { CheckboxInput } from "./checkbox";

describe("CheckboxInput", () => {
  it("uses the shared md size and primary tone by default", () => {
    const markup = renderToStaticMarkup(<CheckboxInput checked readOnly />);

    expect(markup).toContain("size-4");
    expect(markup).toContain("accent-accent-primary");
  });

  it("supports compact size and tone variants", () => {
    const markup = renderToStaticMarkup(
      <CheckboxInput checked readOnly size="xs" tone="gold" />,
    );

    expect(markup).toContain("size-3");
    expect(markup).toContain("accent-accent-gold");
  });
});
