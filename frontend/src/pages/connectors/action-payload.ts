// 通道动作载荷的展示助手（纯函数，不依赖 React，便于单测）。
//
// 两类职责：
//   * qr_code_content 是否**本身**就是可直接 <img> 的图片源；
//   * kind=button 的动作结果压成一行可读摘要（常见字段优先，其余退化为 raw JSON）。

import type { ConnectorActionPayload } from "@/types/runtime";

const IMAGE_EXTENSION = /\.(png|jpe?g|gif|webp|bmp|svg)(?:[?#]|$)/i;

/**
 * 内容本身是否可直接用作 <img> 源：`data:image/` 前缀，或带图片后缀的 http(s) URL。
 *
 * 注意：不能按「含 qrcode 字样」判定——微信扫码返回的 `qr_code_content` 是
 * `https://liteapp.weixin.qq.com/q/…?qrcode=…` 这类**待编码的链接**，含 qrcode
 * 关键字但不是图片；把它当图片会渲染成坏图（正确做法是用二维码库本地编码）。
 */
export function isDirectImageSource(value: string): boolean {
  const text = value.trim();
  if (!text) {
    return false;
  }
  if (/^data:image\//i.test(text)) {
    return true;
  }
  return /^https?:\/\//i.test(text) && IMAGE_EXTENSION.test(text);
}

/** 动作返回的单行摘要；无可用信息时返回空串（调用方改用「无返回内容」文案）。 */
export function summarizeActionPayload(payload: ConnectorActionPayload): string {
  const parts: string[] = [];
  if (payload.message) {
    // 通用人读摘要优先（QQ「授权校验」等 kind=button 动作的主要展示）。
    parts.push(payload.message);
  }
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
