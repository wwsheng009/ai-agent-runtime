import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { ConfigFormField } from "./config-form-field";

describe("ConfigFormField", () => {
  it("associates a single native form control with a label", () => {
    const markup = renderToStaticMarkup(
      <ConfigFormField label="扩展字段 JSON">
        <textarea className="editor" />
      </ConfigFormField>,
    );

    expect(markup.startsWith("<div")).toBe(true);
    expect(markup).toMatch(/<label for="([^"]+)"[^>]*>扩展字段 JSON<\/label>/);

    const labelMatch = markup.match(/<label for="([^"]+)"[^>]*>扩展字段 JSON<\/label>/);
    expect(labelMatch).not.toBeNull();
    expect(markup).toContain(`<textarea class="editor" id="${labelMatch?.[1]}"></textarea>`);
  });

  it("does not wrap composite content in a form label", () => {
    const markup = renderToStaticMarkup(
      <ConfigFormField label="group">
        <div className="space-y-2">
          <select className="editor" />
          <input className="editor" />
        </div>
      </ConfigFormField>,
    );

    expect(markup).not.toContain("<label for=");
    expect(markup).toContain(">group</div>");
  });
});
