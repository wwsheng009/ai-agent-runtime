import { describe, expect, it } from "vitest";

import { buttonVariants } from "./button-variants";
import { fieldControlVariants } from "./field-variants";

describe("button and field size alignment", () => {
  it("uses the same height and typography axis for sm, md, and lg", () => {
    const expected = {
      sm: { height: "h-8", text: "text-xs" },
      md: { height: "h-9", text: "text-sm" },
      lg: { height: "h-10", text: "text-sm" },
    } as const;

    for (const size of ["sm", "md", "lg"] as const) {
      const button = buttonVariants({ size });
      const field = fieldControlVariants({ size });

      expect(button).toContain(expected[size].height);
      expect(field).toContain(expected[size].height);
      expect(button).toContain(expected[size].text);
      expect(field).toContain(expected[size].text);
    }
  });
});