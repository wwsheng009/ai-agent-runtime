package main

import (
	"fmt"
	"io"
)

// printUsage is intentionally the short root-page: it names every command and
// points at the command-specific help. The full flag surface belongs to
// `serve -h` (or `init -h`), not to the root page.
func printUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s - remote tool server (MCP over stdio or Streamable HTTP)\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools <command> [options]

Commands:
  serve     Start the MCP server (stdio or HTTP)
  init      Write a ready-to-run config and token
  doctor    Self-check the normalized config and print effective settings
  tools     List the effective tool names
  policy    Inspect shell command policy without executing a command
  version   Print the version
  help      Show this page

Command help:
  aicli-remote-tools serve -h
  aicli-remote-tools init -h
  aicli-remote-tools doctor -h
  aicli-remote-tools tools -h
  aicli-remote-tools policy -h

Common configuration options (serve/doctor/tools/policy):
  --config <file>                 YAML config (flags override file values)
  --workspace <path>              Workspace root; also the default jail root
  --profile <name>                all (default) | readonly | standard | ops | custom
  --allowed-roots <a,b>           Extra absolute roots
  --tools <a,b>                   Explicit tool allowlist
  --allow-all                     Explicitly allow every single command
  --allow-command <cmd>           Repeatable argv-prefix allow rule
  --deny-command <cmd>            Repeatable argv-prefix deny rule (always wins)
  --allow-command-pattern <glob>  Repeatable whole-command glob allow rule
  --deny-command-pattern <glob>   Repeatable whole-command glob deny rule
  --allowed-command-patterns <p1,p2>
                                  Legacy comma-separated allow patterns
  --denied-command-patterns <p1,p2>
                                  Legacy comma-separated deny patterns
  --deny-shell-metachars[=bool]   Refuse pipes/redirection/substitution (default true)

YAML command-policy keys:
  shell.allowedCommands           argv-prefix allow rules
  shell.deniedCommands            argv-prefix deny rules (always win)
  shell.allowedCommandPatterns    whole-command glob allow rules
  shell.deniedCommandPatterns     whole-command glob deny rules
  shell.denyShellMetachars        separate metacharacter gate

Run 'aicli-remote-tools <command> -h' for the complete command help.
`)
}

func printServeUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s serve - run the remote MCP server\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools serve --stdio [options]
  aicli-remote-tools serve --http  [options]
  aicli-remote-tools serve [options]          # defaults to --http

Workspace and tool surface:
  --workspace <path>              Workspace root (required unless set in config)
  --allowed-roots <a,b>           Extra absolute roots (workspace is always included)
  --profile <name>                all (default) | readonly | standard | ops | custom
  --tools <a,b>                   Explicit tool allowlist (narrows the profile)
  --tool-prefix <prefix>          Prefix every exposed tool name
  --target-name <name>            Label used in descriptions and audit records
  --description-style <style>     compact (default) | explicit
  --shell <path>                  shell for shell/job_start (default: auto-detect)
  --max-output-bytes <n>          Model-visible output cap per call (default 262144)
  --max-bytes-per-call <n>        Per-call payload cap (default 2097152)
  --audit <path>                  NDJSON audit log (0600)
  --audit-command-preview-bytes <n>
                                  Record the first N command bytes in audit (0=off)
  --jobs-root <path>              Legacy job_* store root (requires --legacy-jobs)
  --legacy-jobs                   Re-enable the legacy remote job_* tools
  --artifacts-root <path>         Enable artifact_read for folded captures
  --artifacts-max-bytes <n>       Per-artifact size limit (default 67108864)

Command policy:
  --allow-all                     Explicitly allow every single command; same as
                                  --allow-command "*"; deny, metachar and
                                  destructive gates still apply
  --allow-command <cmd>           Repeatable argv-prefix allow rule; a bare
                                  program allows its arguments; "*" means all
  --deny-command <cmd>            Repeatable argv-prefix deny rule; always wins
  --allow-command-pattern <glob>  Repeatable whole-command glob allow rule
  --deny-command-pattern <glob>   Repeatable whole-command glob deny rule
  --allowed-command-patterns <p1,p2>
                                  Legacy comma-separated allow patterns
  --denied-command-patterns <p1,p2>
                                  Legacy comma-separated deny patterns
  --deny-shell-metachars[=bool]   Refuse pipes, redirection, sequencing and
                                  substitution when true (default true)

YAML mapping:
  --allow-all                     shell.allowedCommands: ["*"]
  --allow-command                 shell.allowedCommands
  --deny-command                  shell.deniedCommands
  --allow-command-pattern         shell.allowedCommandPatterns
  --deny-command-pattern          shell.deniedCommandPatterns
  --deny-shell-metachars          shell.denyShellMetachars

Transport and HTTP:
  --stdio                         MCP over stdin/stdout (SSH transport)
  --http                          MCP over Streamable HTTP
  --http-listen <addr>            Listen address (default 127.0.0.1:18765)
  --http-path <path>              MCP endpoint path (default /mcp)
  --http-token-env <name>         Env var holding the bearer token
  --http-token-file <file>        Read the bearer token from a 0600 file
  --http-access-log <file>        NDJSON request log (includes 401s)
  --tls-cert <file> --tls-key <file>

Gateway mounts:
  --gateway-url <url>             aicli-gateway endpoint
  --gateway-from-aicli            Reuse persisted gateway settings
  --gateway-aicli-config <file>   Explicit aicli config for the previous flag
  --gateway-token-file <file>     Device token file (0600)
  --gateway-token-env <name>      Device token env var
  --gateway-proxy-url <url>       Proxy for the agent-to-gateway tunnel
  --instance <id>                 Per-instance identity/label
  --gateway-name <name>           Node name shown in the gateway console
  --gateway-self-name <name>      Mount name for this endpoint
  --gateway-self-slug <slug>      Pinned public path segment
  --gateway-identity-file <file>  Persisted mount identity
  --gateway-no-mount-self         Publish only --mount entries
  --mount name=<id>,upstream=<url>[,token_file=<f>][,token_env=<NAME>][,slug=<slug>]
                                  Repeatable extra local MCP endpoint

envd backend (serve --stdio --backend envd):
  --backend envd                  Reach a remote envd daemon over Connect-RPC
  --addr <host:port|url>          envd endpoint (required)
  --user <name>                   OS user envd executes as (required)
  --token-env <name>              Env var holding the X-Access-Token
  --ca-file <pem>                 CA bundle for a TLS envd endpoint
  --call-timeout <duration>       Per-call deadline (default 10m)
  --no-envd-files                 Use spawned processes only, never /files

First-run behaviour:
  A bare 'serve' creates ~/.aicli/remote-tools/config.yaml + mcp.token on
  first run and reuses aicli's persisted gateway.* when present.
  --no-auto-init                  Skip creating the default config/token
  --no-gateway                    Ignore aicli's gateway config (stay local)

Examples:
  cd D:\work
  aicli-remote-tools serve

  aicli-remote-tools init --workspace D:\work

  aicli-remote-tools serve --stdio --workspace /srv/app --tool-prefix remote_dev

  aicli-remote-tools serve --http --workspace /srv/app \
    --allow-command git --allow-command "go test" \
    --deny-shell-metachars=true
`)
}

func printDoctorUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s doctor - self-check and print effective settings\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools doctor [options]

doctor accepts the same config/workspace/override options as serve
(without starting a server) and adds:
  --json                          Machine-readable report

The report includes effective allow/deny rules, shell gates, lint findings,
tool surface, writable roots and HTTP/token checks. It does not execute any
shell command.
`)
}

func printToolsUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s tools - list effective tool names\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools tools [options]

tools accepts the same config/workspace/override options as serve and adds:
  --json                          Machine-readable output
`)
}

func printPolicyUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s policy - inspect shell command policy\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools policy test [options] -- <command>
  aicli-remote-tools policy test [options] "<command>"
  aicli-remote-tools policy lint [options]

Commands:
  test    Evaluate one command through deny -> allow -> metachar -> destructive
          gates. It never executes the command.
  lint    Check the effective shell configuration for common mistakes and
          high-risk combinations.

Options:
  policy test and policy lint accept the same config/workspace/override flags
  as serve, including --allow-all, --allow-command, --deny-command,
  --allow-command-pattern, --deny-command-pattern, --allowed-command-patterns,
  --denied-command-patterns, --deny-shell-metachars and --json.

Examples:
  aicli-remote-tools policy test --workspace D:\work "git status"
  aicli-remote-tools policy test --config remote.yaml -- "deploy-a.sh --force"
  aicli-remote-tools policy lint --config remote.yaml
  aicli-remote-tools policy lint --json --config remote.yaml
`)
}

func printPolicyTestUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s policy test - evaluate one command\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools policy test [options] -- <command>
  aicli-remote-tools policy test [options] "<command>"

Options are the same as serve's config/workspace/override flags.
The command is evaluated but never executed. Exit status is 0 when the
evaluation completes; inspect decision=allow|deny in the output.
`)
}

func printPolicyLintUsage(w io.Writer) {
	fmt.Fprintf(w, "aicli-remote-tools %s policy lint - check shell policy configuration\n\n", version)
	fmt.Fprint(w, `Usage:
  aicli-remote-tools policy lint [options]

Options are the same as serve's config/workspace/override flags.
Findings are printed one per line; exit status is non-zero when findings exist.
`)
}
