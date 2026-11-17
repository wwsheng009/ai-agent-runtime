import { describe, expect, it } from "vitest";

import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "./field-variants";

describe("field variants", () => {
  it("uses a fixed height and visible focus for single-line fields", () => {
    const className = fieldControlVariants({ size: "md" });

    expect(className).toContain("h-9");
    expect(className).toContain("focus-visible:ring-2");
    expect(className).toContain("focus-visible:ring-ring");
    expect(className).toContain("rounded-field");
  });

  it("keeps textareas height-flexible and uses the md density by default", () => {
    const className = fieldTextareaVariants();

    expect(className).toContain("min-h-28");
    expect(className).not.toContain("h-9");
    expect(className).toContain("focus-visible:ring-2");
  });
});
