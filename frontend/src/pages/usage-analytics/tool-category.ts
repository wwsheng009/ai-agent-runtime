export type ToolCategory = "builtin" | "mcp" | "shell";

const shellToolNames = new Set(["shell", "bash", "execute_shell_command", "aicli_exec"]);

export function classifyToolCategory(toolName: string): ToolCategory {
  const normalized = toolName.trim().toLowerCase();
  if (normalized.startsWith("mcp__")) return "mcp";
  if (shellToolNames.has(normalized)) return "shell";
  return "builtin";
}
