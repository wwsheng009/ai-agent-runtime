// P1-6：工具行链接契约。宿主解析出可激活回调才渲染交互链接，避免出现死按钮。

export type FilePathLink = {
  path: string;
  activate: () => void;
};

const EXTERNAL_URL_PATTERN = /^https?:\/\//i;

export function isExternalUrl(url: string) {
  return EXTERNAL_URL_PATTERN.test(url.trim());
}
