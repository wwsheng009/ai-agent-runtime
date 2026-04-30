import { afterEach } from "vitest";

import { initI18n, i18n } from "@/i18n";

initI18n("zh-CN");

afterEach(() => {
  if (i18n.isInitialized) {
    void i18n.changeLanguage("zh-CN");
  }
});
