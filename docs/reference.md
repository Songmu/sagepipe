# sagepipe reference

For a quick start and examples, see the [README](../README.md).

## Records and modes

The presence of `input_schema` makes input JSONL; its absence makes input
line-oriented text. `output_schema` independently selects JSONL or text
output. JSONL records may contain any JSON value, not just objects. A JSONL
blank or spaces-and-tabs-only line is skipped; an empty text line is a record.
Both LF and CRLF input are accepted, and each successful output record ends
in LF. Text output records cannot contain a line break.

`map` processes each input line independently, producing zero or more output
records per line. `reduce` processes the valid input records together, also
producing zero or more output records. In the default `auto` mode, a nonempty
prompt and at least one valid input record cause one additional agent call to
choose `map` or `reduce`; ambiguous transformations use `map`. Specify a mode
to avoid this extra call and make the processing unit predictable. Requests
are processed sequentially by default. Set `concurrency` in
frontmatter or pass `--concurrency N` to process up to N `map` records at once.
Successful results remain in input order, even if later agent calls finish
first. At most N records and their pending results are held before writing.
Each call is independent; ACP uses separate connections for concurrent calls,
while CLI agents use separate subprocesses. Explicit `reduce` mode rejects
concurrency greater than 1. In `auto` mode, a `reduce` decision runs the
single aggregate call sequentially even when concurrency is greater than 1.

Invalid input rows and failed `map` calls are reported on standard error
without inserting error records into standard output. For `reduce`, invalid
input rows are excluded but still count as failures; a failed aggregate call
produces no aggregate output.

## Responses and retries

The agent is instructed to return a single JSON object of the form
`{"items":[...]}`. sagepipe checks the entire answer and validates every item
before writing any result from that call. As a recovery measure, a valid bare
item is treated as one output record. A bare array is treated as zero or more
output records when all its elements are valid; otherwise, if the array itself
satisfies `output_schema`, it is treated as one array-valued record. These cases
produce `bare_item_normalized` or `bare_array_normalized` warnings. A response
consisting solely of one `json` or unlabelled Markdown code block is unwrapped
before validation and produces a `markdown_fence_removed` warning; surrounding
prose remains invalid.

Each agent request is retried at most twice after an empty response. Incomplete
JSON is retried with a repair request, while other responses that cannot be
decoded as JSON are retried with a format-correction request. Both requests
contain the original request, invalid response, and validation error as
JSON-encoded data. Complete JSON that cannot be accepted as the canonical
envelope, a valid bare item, or a valid bare array is retried with an
output-correction request. Retry attempts produce `agent_retry` warnings, and
the final summary includes `retries` and `recovered` counts.

If tools are enabled, a retry can repeat tool side effects from the failed
request; configure tool access accordingly.

## Exit status and diagnostics

Exit status is `0` on success, `1` after completing input with rejected
records, and `2` for failures that prevent completing the run. Standard
error contains JSONL diagnostics with `level`, `code`, `stage`, and `message`;
input-specific diagnostics also include the one-based physical `line`.
With `-i` or `--ignore-failures`, writing at least one valid output record
changes a nonzero exit status to `0`, even if processing subsequently fails
(including input read failures or cancellation). No output records, including
a valid empty result, leave the usual exit status unchanged. Error diagnostics
are still emitted; this flag does not skip processing or suppress failures.
By default, only WARN and ERROR diagnostics are emitted. `-v` adds INFO
diagnostics; `-vv` (or repeating `--verbose`) also adds DEBUG diagnostics.
When available, INFO `agent_usage` diagnostics include `model`, `model_source`,
and a human-readable `model_name`; agents without usage data emit an
`agent_model` diagnostic instead. Explicit selections use the `model_source`
value `explicit`, while ACP defaults discovered from session configuration use
`session_config`. Unreported default models are not inferred from agent
response text. For invalid agent output, DEBUG diagnostics include the
validation reason and up to 4096 bytes of the raw response. Agent call
failures similarly include the underlying error at DEBUG level.

## Agents and permissions

`copilot`, `claude`, and `codex` default to their respective non-interactive
CLIs. Install and authenticate the selected agent separately. Copilot's
experimental ACP connection remains available with `--protocol acp`. There
is no automatic fallback between agents or protocols. `agent.args` adds
arguments before sagepipe's required launch arguments for built-in agents:

```yaml
agent:
  provider: copilot
  args:
    - --disable-builtin-mcps
    - --disable-mcp-server=workiq
```

Built-in agent arguments cannot contain a standalone `--`, because sagepipe
appends required protocol and machine-readable output arguments after them.

A custom ACP agent can be configured with a command and arguments:

```yaml
agent:
  protocol: acp
  command: my-agent-acp
  args: [--stdio]
  cwd: ./agent-project
```

`-vv` includes an `agent_process_starting` DEBUG
diagnostic with each subprocess command, raw arguments, and working directory.
In `auto` mode, it also includes a `mode_reason` DEBUG diagnostic with the
agent's unredacted rationale; the INFO `mode_selected` diagnostic retains a
fixed, safe reason. Invalid-response details may contain raw agent output. Raw
arguments may contain custom agent arguments, tool rules, or credentials;
rationale and agent output may contain prompts, input data, or schemas. These
details are omitted by default and with `-v`. Copilot CLI prompts are always
sent through standard input and are not included in launch arguments.

> [!WARNING]
> Enable DEBUG diagnostics only in trusted environments. Prefer environment
> variables or protected files over command-line arguments for sensitive data.
> Before running with `-vv` in CI, register every sensitive value with the
> CI system's log-masking mechanism. In GitHub Actions, emit
> `::add-mask::{value}` before any command can print that value:
>
> ```yaml
> - name: Mask generated credentials
>   run: |
>     echo "::add-mask::$MCP_CREDENTIAL"
> ```
>
> Register masks before invoking sagepipe and repeat this for each sensitive
> value in every job where verbose diagnostics may be captured.

`allowed-tools` is a top-level, space-separated YAML frontmatter value
compatible in spelling with Agent Skills. It is passed through the
selected agent's native tool mechanism, not enforced as a portable sandbox:

```yaml
agent: claude
allowed-tools: Read Grep Glob
```

An explicit `--allowed-tools 'Read Grep Glob'` overrides the configured value.
Changing the agent with `--agent` does not clear the top-level value, even
if the tool names are not suitable for the new agent. An agent or protocol
without a way to specify the requested tools fails instead of silently
ignoring the option. Without this setting, the agent's own defaults apply;
read-only behavior, file isolation, and network isolation are not guaranteed.
Agent processes inherit the invoking process's environment.

## Schemas and paths

Schema values in the configuration may be inline JSON Schema objects, boolean
schemas, or file paths; string values are always paths. A CLI schema flag
accepts a JSON object or `true`/`false` directly, or a path otherwise.
Local file references in schemas are supported; external HTTP(S) references
are not fetched. Schema validation supports Draft-04, Draft-06, Draft-07,
Draft 2019-09, and Draft 2020-12, subject to the selected draft's rules.
The selected agent's generation-time schema support is only a hint; every
result is also validated locally.

Configuration-relative paths resolve from the configuration file's directory.
`-C <dir>` and `--cwd <dir>` change the sagepipe process's working directory
and the base for a relative `--config` path. Other relative CLI paths resolve
from that directory. The agent inherits it only when `agent.cwd` is unset;
`agent.cwd` or `--agent-cwd` changes only the agent's working directory. A
relative `--agent-cwd` value is passed through relative to the changed process
directory, while configuration-file `agent.cwd` remains configuration-relative.

## Options and limits

| Flag | Default | Effect |
| --- | --- | --- |
| `--agent`, `--protocol`, `--model` | `copilot`, agent's default protocol/model | Select an agent connection |
| `--mode` | `auto` | Select `map`, `reduce`, or `auto` |
| `--concurrency` | `1` | Maximum simultaneous `map` requests; positive integer |
| `--input-schema`, `--output-schema` | None | Validate records and select JSONL on that side |
| `--max-line-bytes` | 1,048,576 | Maximum raw input line in `map` |
| `--max-input-bytes` | 65,536 | Maximum total raw input in `reduce` |
| `--max-response-bytes` | 8,388,608 | Maximum final answer per agent call |
| `--timeout` | None | Deadline for each agent call, including auto-mode selection |
| `-i`, `--ignore-failures` | Off | Exit `0` after writing at least one valid output record, even on failure |
| `-v`, `--verbose` | WARN diagnostics | Show INFO diagnostics; repeat (`-vv`) for DEBUG diagnostics including raw agent launch arguments |
| `-C`, `--cwd` | Invoking directory | Set the sagepipe process directory |
| `--agent-cwd` | Effective `cwd` | Set only the agent directory |

Limits can also be set in frontmatter as `max_line_bytes`,
`max_input_bytes`, `max_response_bytes`, and `timeout`. An oversized
`reduce` input fails instead of being truncated or split. Empty responses and
responses that cannot be decoded as JSON may cause up to two automatic retries.

Ctrl-C (and SIGTERM on Unix) cancels processing and attempts to close standard
input to release a pending read. The run reports a JSONL cancellation diagnostic
and exits with status 2 after agent cleanup (or 0 with `--ignore-failures` if
at least one valid output record was already written). On Unix, CLI and ACP agent
subprocess groups are terminated on cancellation. On Windows, CLI waiting is
bounded even when descendants keep its output pipes open, but only the direct
CLI or ACP agent process is guaranteed to be terminated; descendants may
continue running.
