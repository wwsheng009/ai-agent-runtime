# Tool selection notes for the coding profile.
# This file is profile-level guidance. The built-in system prompt deliberately
# stays neutral about which search tool to use; the choices below belong here so
# they can be tuned per profile instead of being hard-coded into the runtime.

## Symbol-level questions: use the code.* index tools

This profile enables the index-backed `code.*` tools
(`code_search` / `code_inspect` / `code_navigate` / `code_references` /
`code_callers`). Reach for them when the question is about symbols rather than
free text:

- "Where is X defined?" / "which symbols match this name?" → `code_search`
- "Show me X's implementation" (you know the name, not the line) → `code_inspect`
- "What symbols does this file define?" → `code_navigate` (direction=members)
- "Who calls X?" → `code_callers` (or `code_navigate` direction=refs)
- "Who references X?" (any reference kind) → `code_references`

These tools resolve the symbol graph from a local index, so they answer
"definition / caller / reference" questions more precisely than a text match.
Read the returned `confidence` and `source` fields: results below 0.9 confidence
or `source=fallback` are approximations and should be confirmed with `grep`
before you rely on them.

## Text, config, and log search: use grep

For literal or pattern search over file contents ("where does this string
appear", "find this config key", "grep the logs"), use `grep`/`rg`. That is
still a fine default for most searching; the `code.*` tools are for symbol
structure specifically.

## Ordinary reads: use view

To read a file, a line range, or several files at once, use `view`. You do not
need a code.* tool just to read source.
