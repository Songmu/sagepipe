sagepipe
=======

[![Test Status](https://github.com/Songmu/sagepipe/actions/workflows/test.yaml/badge.svg?branch=main)][actions]
[![Coverage Status](https://codecov.io/gh/Songmu/sagepipe/branch/main/graph/badge.svg)][codecov]
[![MIT License](https://img.shields.io/github/license/Songmu/sagepipe)][license]
[![PkgGoDev](https://pkg.go.dev/badge/github.com/Songmu/sagepipe)][PkgGoDev]

[actions]: https://github.com/Songmu/sagepipe/actions?workflow=test
[codecov]: https://codecov.io/gh/Songmu/sagepipe
[license]: https://github.com/Songmu/sagepipe/blob/main/LICENSE
[PkgGoDev]: https://pkg.go.dev/github.com/Songmu/sagepipe

sagepipe is a Unix-style filter that asks an AI agent to transform records
from standard input and writes successful results to standard output. It can
read and write either JSON Lines (JSONL) or line-oriented text.

## Quick start

```console
printf 'hello\n' | sagepipe --agent claude --mode map --prompt 'Translate to Japanese'
```

For structured output, put YAML frontmatter and transformation instructions
in a Markdown configuration file:

````markdown
---
agent: copilot
mode: map
input_schema:
  type: object
  required: [name]
  properties:
    name:
      type: string
output_schema:
  type: object
  required: [category]
  properties:
    category:
      type: string
---

Classify each input name. Do not invent names.
````

```console
cat input.jsonl | sagepipe --config config.md > output.jsonl
```

`--config` is optional; `--prompt` overrides the entire Markdown body, including
when passed an empty string. Without an explicit prompt, an empty prompt is
used. CLI flags take precedence over configuration values.

## Records and modes

The presence of `input_schema` makes input JSONL; its absence makes input
line-oriented text. `output_schema` independently selects JSONL or text
output. JSONL records may contain any JSON value, not just objects.

`map` processes each input line independently, producing zero or more output
records per line. `reduce` processes the valid input records together, also
producing zero or more output records. The default `auto` mode asks the agent
to choose between them, which can add a call; specify a mode to make the
processing unit predictable. Requests are sequential by default; use
`--concurrency N` for parallel `map` requests. Results remain in input order.

Failed records are reported on standard error without adding error records to
standard output. Exit status is `0` on success, `1` for rejected records after
processing input, and `2` when the run cannot complete. See the
[reference](docs/reference.md) for exact record handling, response validation,
retries, diagnostics, and concurrency behavior.

## Agents and permissions

`copilot`, `claude`, and `codex` default to their respective non-interactive
CLIs. Install and authenticate the selected agent separately. Copilot's
experimental ACP connection remains available with `--protocol acp`. There
is no automatic fallback between agents or protocols. Other agents can be
connected through a custom ACP command.

For example, [OpenCode](https://opencode.ai/docs/acp/) exposes an ACP server
with `opencode acp`:

```yaml
agent:
  protocol: acp
  command: opencode
  args: [acp]
```

Install and authenticate OpenCode separately before running sagepipe. OpenCode
loads its normal project configuration, including model and permission
settings. Generic ACP agents do not support sagepipe's `allowed-tools` option;
configure tool permissions in OpenCode instead.

`allowed-tools` is a top-level, space-separated YAML frontmatter value
compatible in spelling with Agent Skills. It is passed through the
selected agent's native tool mechanism, not enforced as a portable sandbox:

```yaml
agent: claude
allowed-tools: Read Grep Glob
```

Without this setting, the agent's own defaults apply; read-only behavior, file
isolation, and network isolation are not guaranteed. Agent processes inherit
the invoking process's environment. Retries can repeat tool side effects.
Only enable `-vv` in trusted environments: DEBUG diagnostics can contain raw
agent arguments, prompts, and responses. See the [reference](docs/reference.md#agents-and-permissions)
for permission and log-masking details.

## Configuration

| Option | Default | Purpose |
| --- | --- | --- |
| `--config` | None | Read YAML frontmatter and prompt from Markdown |
| `--agent`, `--protocol`, `--model` | `copilot`, agent's default protocol/model | Select an agent connection |
| `--mode` | `auto` | Select `map`, `reduce`, or `auto` |
| `--concurrency` | `1` | Maximum simultaneous `map` requests |
| `--input-schema`, `--output-schema` | None | Validate records and select JSONL on that side |
| `-C`, `--cwd`, `--agent-cwd` | Invoking directory | Set the process or agent working directory |
| `--timeout`, `--max-line-bytes`, `--max-input-bytes`, `--max-response-bytes` | See reference | Limit agent calls and record sizes |
| `-v`, `--verbose` | WARN diagnostics | Include INFO; repeat for DEBUG |

See the [reference](docs/reference.md) for all defaults, frontmatter settings,
path resolution, record and mode semantics, responses, retries, and diagnostics.

## Installation

```console
# Install the latest version. (Install it into ./bin/ by default).
% curl -sfL https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s

# Specify installation directory ($(go env GOPATH)/bin/) and version.
% curl -sfL https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s -- -b $(go env GOPATH)/bin [vX.Y.Z]

# In alpine linux (as it does not come with curl by default)
% wget -O - -q https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s [vX.Y.Z]

# go install
% go install github.com/Songmu/sagepipe/cmd/sagepipe@latest
```

## Author

[Songmu](https://github.com/Songmu)
