import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Input } from "./input";
import { Select } from "./select";
import { Textarea } from "./textarea";

describe("field controls", () => {
  it("renders single-line controls on the shared md size axis", () => {
    const inputMarkup = renderToStaticMarkup(<Input aria-label="name" />);
    const selectMarkup = renderToStaticMarkup(
      <Select
        ariaLabel="kind"
        options={[{ value: "a", label: "A" }]}
        value="a"
        onChange={() => undefined}
      />,
    );

    for (const markup of [inputMarkup, selectMarkup]) {
      expect(markup).toContain("h-9");
      expect(markup).toContain("rounded-field");
      expect(markup).toContain("focus-visible:ring-2");
    }
  });

  it("lets textareas keep a flexible height", () => {
    const markup = renderToStaticMarkup(<Textarea aria-label="plan" />);

    expect(markup).toContain("min-h-28");
    expect(markup).not.toContain("h-9");
  });
});
