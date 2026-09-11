import { defineConfig } from "@playwright/test";

import baseConfig from "./playwright.config";

// Manual debug specs (`*.manual.ts`) are excluded from the default e2e run:
// `playwright.config.ts` keeps Playwright's `*.spec.ts` testMatch. Run on demand:
//   npm run test:manual -- e2e/diag.manual.ts
// The mock + dev servers and browser channel are inherited from the base config.
export default defineConfig({
  ...baseConfig,
  testMatch: /.*\.manual\.ts$/,
});
