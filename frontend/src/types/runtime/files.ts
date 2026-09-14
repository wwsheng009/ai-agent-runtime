// P2-1A：运行时文件读取（POST /api/runtime/fs/read-file）类型。
//
// 后端契约（backend/internal/api/skills/file_transfer_handlers.go:36-64）：
//   请求体（JSON）：{path}
//   响应体：{file: {path(后端解析出的绝对路径), data_base64(标准 base64), byte_count}}
//   错误体：{error, request_id?}；未注入 filetransport 服务时 503。
//
// 归一化纪律：`file` 结构缺失或字段类型不符 → 抛错（不把结构异常伪装成空文件）。

export type RuntimeFileReadResult = {
  /** 后端解析后的绝对路径（相对路径按运行时进程工作目录解析）。 */
  path: string;
  dataBase64: string;
  byteCount: number;
};
