import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { LandingPage } from "./landing-page";

describe("LandingPage", () => {
  it("renders the hero immediately while deferring below-the-fold sections", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    );

    expect(markup).toContain("Enter workspace");
    expect(markup).toContain("Research, orchestrate, and ship");
    expect(markup).not.toContain(
      "See how the workspace turns agent work into something reviewable",
    );
    expect(markup).not.toContain(
      "Bring teams, evidence, and runtime control into one flow",
    );
  });
});
