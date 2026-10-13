// 通道动作载荷的展示助手（纯函数，不依赖 React，便于单测）。
//
// 两类职责：
//   * qr_code_content 是图片源（data:image/ 或「看起来像图片」的 URL）还是文本；
//   * kind=button 的动作结果压成一行可读摘要（常见字段优先，其余退化为 raw JSON）。

import type { ConnectorActionPayload } from "@/types/runtime";

const IMAGE_EXTENSION = /\.(png|jpe?g|gif|webp|bmp|svg)(?:[?#]|$)/i;
const IMAGE_HINT = /(qrcode|qr[_-]?code|qrcode_img)/i;

/** 是否可用 <img> 直接展示：data:image/ 前缀，或带图片特征（后缀/命名）的 http(s) URL。 */
export function isImageSource(value: string): boolean {
  const text = value.trim();
  if (!text) {
    return false;
  }
  if (/^data:image\//i.test(text)) {
    return true;
  }
  if (!/^https?:\/\//i.test(text)) {
    return false;
  }
  return IMAGE_EXTENSION.test(text) || IMAGE_HINT.test(text);
}

/** 动作返回的单行摘要；无可用信息时返回空串（调用方改用「无返回内容」文案）。 */
export function summarizeActionPayload(payload: ConnectorActionPayload): string {
  const parts: string[] = [];
  if (payload.status) {
    parts.push(`status=${payload.status}`);
  }
  if (payload.login_id) {
    parts.push(`login_id=${payload.login_id}`);
  }
  if (payload.account_id) {
    parts.push(`account_id=${payload.account_id}`);
  }
  if (parts.length > 0) {
    return parts.join(" · ");
  }
  const raw = JSON.stringify(payload.raw);
  return raw === "{}" ? "" : raw;
}
