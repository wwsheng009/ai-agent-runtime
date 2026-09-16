// P1-6：工具名 → 专属卡片注册表。未注册走 generic 卡；注册只影响呈现，不伪造数据。

export type ToolCardKind =
  | "read"
  | "diff"
  | "terminal"
  | "search"
  | "list"
  | "web"
  | "image"
  | "json"
  | "generic";

const KIND_BY_NAME: Record<string, ToolCardKind> = {
  read_file: "read",
  readfile: "read",
  read: "read",
  cat: "read",
  view: "read",
  view_file: "read",
  fs_read: "read",
  fs_read_file: "read",
  get_file_content: "read",

  apply_patch: "diff",
  edit: "diff",
  edit_file: "diff",
  editfile: "diff",
  str_replace: "diff",
  str_replace_editor: "diff",
  write: "diff",
  write_file: "diff",
  writefile: "diff",
  create_file: "diff",
  append_file: "diff",
  fs_write_file: "diff",
  fs_append_file: "diff",
  notebook_edit: "diff",

  shell: "terminal",
  run_command: "terminal",
  run_terminal_cmd: "terminal",
  execute_command: "terminal",
  exec: "terminal",
  bash: "terminal",
  sh: "terminal",
  zsh: "terminal",
  powershell: "terminal",
  pwsh: "terminal",
  cmd: "terminal",
  terminal: "terminal",

  grep: "search",
  rg: "search",
  ripgrep: "search",
  glob: "search",
  find: "search",
  search: "search",
  search_files: "search",
  code_search: "search",

  // 目录列举类（运行时内置 `ls` 等）：目标是**目录**而不是文件，摘要显示目录本身；
  // 目录不进文件链接（见 details.directoryPath）。
  ls: "list",
  listdir: "list",
  list_dir: "list",
  list_directory: "list",
  list_files: "list",
  read_dir: "list",

  web_search: "web",
  websearch: "web",
  web_fetch: "web",
  webfetch: "web",
  fetch: "web",
  fetch_url: "web",
  http_request: "web",
  browser: "web",
  browse: "web",

  image: "image",
  image_generation: "image",
  generate_image: "image",
  create_image: "image",
  view_image: "image",
  read_image: "image",

  json: "json",
  read_json: "json",
  parse_json: "json",
  jq: "json",
};

function normalizeToolName(name: string) {
  return name.trim().toLowerCase().replace(/[\s-]+/g, "_");
}

/** 对 `mcp__server__read_file` 这类命名空间前缀取最后一段，命中不了退 generic。 */
function candidates(name: string) {
  const normalized = normalizeToolName(name);
  const parts = normalized.split(/__|::|\//).filter(Boolean);
  return parts.length > 1 ? [normalized, parts[parts.length - 1]] : [normalized];
}

export function resolveToolCardKind(name: string): ToolCardKind {
  for (const candidate of candidates(name)) {
    const kind = KIND_BY_NAME[candidate];
    if (kind) {
      return kind;
    }
  }
  return "generic";
}
