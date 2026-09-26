// S5（跨端图片附件方案 §3.2）：运行时图片附件「上传 → 带图发送」的契约类型。
//
// 后端就绪证据（纯前端接线，不改 Go）：
//   * `POST /api/runtime/uploads`（multipart/form-data，字段 `file`，≤8 份）
//     → `{ok, accepted, attachments:[{name,path,bytes,width,height,note,skipped}]}`
//     上传只落盘，不改会话状态；
//   * `POST /api/runtime/sessions/{id}/runtime/commands` 的
//     `{type:"submit_prompt", prompt, images:[path]}` → 响应按需附带
//     `{attached_images, image_notes}`（不带 images 时字段不出现，旧契约不变）。
//
// 归一化纪律：结构异常（`attachments` 非数组、缺 `name`）一律抛错，
// 不伪造「空上传成功」；字段缺失按后端 `omitempty` 语义给 0 / 空串。

export type RuntimeUploadAttachment = {
  /** 原始文件名（后端回显的上传文件名）。 */
  name: string;
  /** 服务端落盘路径；被跳过的条目没有路径 → 空串（不伪造一个假路径）。 */
  path: string;
  bytes: number;
  width: number;
  height: number;
  /** 后端说明（跳过原因 / 压缩说明）；无说明为空串。 */
  note: string;
  /** 后端判定为不可用（超体积上限 / 不是可识别图片）时 true。 */
  skipped: boolean;
};

export type RuntimeUploadResult = {
  /** 后端 `ok` 字段原值。 */
  ok: boolean;
  /** 后端上报的接受数（不按数组长度改写）。 */
  accepted: number;
  /** 逐条处理结果（顺序即后端返回顺序）。 */
  attachments: RuntimeUploadAttachment[];
};

export type RuntimeSessionPromptResult = {
  /** true = 会话忙，本轮未执行（后端 202 `{ok, pending, state}`）。 */
  pending: boolean;
  /** 本回合实际挂上的图片数；响应未带该字段时为 null（而不是伪造 0）。 */
  attachedImages: number | null;
  /** 后端逐条图片说明（拒绝原因 / 预处理说明），未带该字段时为空数组。 */
  imageNotes: string[];
  /** 后端 `result` 原文；pending 或未返回时为 null。 */
  result: unknown;
};
