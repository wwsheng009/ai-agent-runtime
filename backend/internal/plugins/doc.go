// Package plugins provides local plugin packaging for harness productization (Iteration C2).
//
// A plugin is a directory with plugin.yaml (or plugin.yml / plugin.json) that may bundle:
//   - skills/   — skill trees loadable by the existing skill loader
//   - agents/   — portable agent definitions (agentdef)
//   - hooks.yaml or hooks/ — runtime hook configs
//   - mcp.yaml / mcp.json — MCP server descriptors (mcpServers map)
//
// Design constraints:
//   - Does not rebuild skill loaders or team orchestration.
//   - Trust is local-only (state file under ~/.aicli/plugins); no marketplace.
//   - Hot-load means skill dirs from trusted enabled plugins are merged into
//     the existing skills-dir resolution path (and can be watched by skill HotReload).
package plugins
