import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Input } from "./input";
import { NativeSelect } from "./native-select";
import { Textarea } from "./textarea";

describe("field controls", () => {
  it("renders single-line controls on the shared md size axis", () => {
    const inputMarkup = renderToStaticMarkup(<Input aria-label="name" />);
    const selectMarkup = renderToStaticMarkup(
      <NativeSelect aria-label="kind">
        <option value="a">A</option>
      </NativeSelect>,
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
