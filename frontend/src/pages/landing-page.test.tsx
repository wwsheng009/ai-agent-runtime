import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { CommunitySection } from "@/components/landing/community-section";
import { LandingFooter } from "@/components/landing/landing-footer";

import { LandingPage } from "./landing-page";

describe("LandingPage", () => {
  it("renders the hero immediately while deferring below-the-fold sections", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    );

    expect(markup).toContain("进入工作台");
    expect(markup).toContain("研究、编排并交付");
    expect(markup).not.toContain(
      "看工作区如何把 Agent 工作变成可审阅的内容",
    );
    expect(markup).not.toContain(
      "把团队、证据和运行时控制放进同一条流程",
    );
  });

  it("keeps landing calls to action as single accessible links", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <CommunitySection />
        <LandingFooter />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/workspace"');
    expect(markup).toContain('href="#product-highlights"');
    expect(markup).not.toMatch(/<a[^>]*>\s*<button/i);
  });
});
