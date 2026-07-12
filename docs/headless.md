# Headless And Automation Use

Headless execution is provided by the official Cursor CLI. `cursor-cli-byok`
preserves Cursor's prompt arguments, stdin, stdout, stderr, signals, and exit
code while replacing only the backend endpoint and authentication context.

Cursor IDE and Cursor login are not required. The selected provider key must be
present in the wrapper environment for every run.

## Text Output

`-p` is the short form of `--print`:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Return a concise repository summary.'
```

The default output format is `text`. Final assistant text is written to stdout.
Warnings and diagnostics belong to stderr.

To retain the channels separately:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Return a concise repository summary.' \
  >result.txt 2>diagnostics.log
```

## JSON Output

Request one result object:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  --output-format json \
  'Return the repository status.' \
  >result.json 2>diagnostics.log
```

A verified successful result has stable semantic fields equivalent to:

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "result": "final assistant text",
  "session_id": "non-empty",
  "request_id": "non-empty",
  "usage": {
    "inputTokens": 1,
    "outputTokens": 1,
    "cacheReadTokens": 0,
    "cacheWriteTokens": 0
  }
}
```

Treat the process exit code as authoritative before trusting the object. Do not
merge stderr into stdout when parsing machine output.

## Stream JSON Output

Request one JSON object per non-empty stdout line:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  --output-format stream-json \
  --stream-partial-output \
  'Return the repository status.' \
  >events.jsonl 2>diagnostics.log
```

The compatibility gate verifies this semantic order for a simple answer:

1. one `system/init` event;
2. one user message event;
3. one or more partial assistant text events;
4. one repeated full assistant message;
5. one terminal `result/success` event.

Partial text concatenates to the same final text carried by the full assistant
message and terminal result. Consumers should not rely on timestamps, exact
token counts, durations, session values, request values, or undocumented
metadata.

## Exit Codes And Cancellation

A zero exit code means the official Cursor CLI completed successfully. Provider
failure, invalid configuration, tool failure, and cancellation return nonzero.
The wrapper does not fall back to Cursor-hosted inference when a custom provider
turn fails.

On Linux, bound an invocation with the system `timeout` command:

```sh
timeout --signal=TERM 120s \
  cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Inspect the repository without changing it.'
```

`SIGINT` and `SIGTERM` propagate through the wrapper to the official CLI and
the active provider request. A timeout or signal is a failed run; do not consume
partial stdout as a successful terminal result.

## Workspace And Permissions

Use `--workspace` to avoid depending on the caller's current directory:

```sh
cursor-cli-byok --workspace /srv/project --trust -p --mode plan \
  'Plan the requested change.'
```

Official Cursor permissions remain explicit:

| Option | Meaning |
| --- | --- |
| `--mode ask` | Read-only Q&A and explanation |
| `--mode plan` or `--plan` | Read-only analysis and planning |
| no mode option | Normal agent behavior; tools are available |
| `--trust` | Trust the selected workspace for this headless run |
| `--force` or `--yolo` | Force-allow commands and destructive file operations unless denied |
| `--approve-mcps` | Automatically approve configured MCP servers |

`-p` itself has access to tools. Select `ask` or `plan` when automation must be
read-only. Use `--force` and `--approve-mcps` only when the caller deliberately
authorizes their side effects and trusts the workspace and MCP definitions.
The wrapper never adds any of these options silently.

## Node.js JSON Example

This dependency-free example uses `execFile`, so the prompt and arguments are
never interpreted by a shell:

```js
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

if (!process.env.OPENAI_API_KEY) {
  throw new Error("OPENAI_API_KEY is required");
}

try {
  const { stdout, stderr } = await execFileAsync(
    "cursor-cli-byok",
    [
      "--workspace",
      process.cwd(),
      "--trust",
      "-p",
      "--mode",
      "ask",
      "--output-format",
      "json",
      "Return a concise repository summary.",
    ],
    {
      env: { ...process.env },
      encoding: "utf8",
      timeout: 120_000,
      killSignal: "SIGTERM",
      maxBuffer: 8 * 1024 * 1024,
    },
  );

  if (stderr) process.stderr.write(stderr);
  const message = JSON.parse(stdout);
  if (
    message.type !== "result" ||
    message.subtype !== "success" ||
    message.is_error !== false
  ) {
    throw new Error("Cursor returned a non-success result");
  }
  process.stdout.write(`${message.result}\n`);
} catch (error) {
  if (error.stderr) process.stderr.write(error.stderr);
  throw error;
}
```

Inject the key through the parent process or secret manager. Do not place a
literal key in source code or an argument array.

## Node.js Stream JSON Outline

Use `spawn` when events should be handled as they arrive:

```js
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";

const child = spawn(
  "cursor-cli-byok",
  [
    "--workspace",
    process.cwd(),
    "--trust",
    "-p",
    "--mode",
    "ask",
    "--output-format",
    "stream-json",
    "--stream-partial-output",
    "Return a concise repository summary.",
  ],
  {
    env: { ...process.env },
    stdio: ["ignore", "pipe", "pipe"],
  },
);

const completed = new Promise((resolve, reject) => {
  child.once("error", reject);
  child.once("exit", (code, signal) => resolve({ code, signal }));
});

child.stderr.pipe(process.stderr);
let terminalResult;
const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
for await (const line of lines) {
  if (!line.trim()) continue;
  const event = JSON.parse(line);
  if (event.type === "result") terminalResult = event;
}

const { code, signal } = await completed;
if (code !== 0) {
  throw new Error(`Cursor failed: code=${code} signal=${signal ?? "none"}`);
}
if (
  terminalResult?.subtype !== "success" ||
  terminalResult?.is_error !== false
) {
  throw new Error("Successful terminal result is missing");
}
process.stdout.write(`${terminalResult.result}\n`);
```

Production consumers can additionally enforce consistent session IDs, partial
text concatenation, non-negative usage, an event-size bound, and no events
after the terminal result. The repository's dependency-free E2E validator does
all of those checks.

## CI Use

Install a pinned wrapper release, inject the provider key as a masked secret,
select a fixed workspace, and preserve machine stdout separately:

```yaml
- name: Run read-only Cursor check
  env:
    OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
  run: |
    cursor-cli-byok --workspace "$GITHUB_WORKSPACE" --trust -p --mode ask \
      --output-format json \
      'Return a concise repository summary.' \
      >cursor-result.json 2>cursor-diagnostics.log
```

Fail the job on any nonzero process exit. Do not make a failed or canceled run
look successful merely because stdout contains partial assistant text.

Parallel jobs that use different credentials must not share one daemon scope.
Give each job separate `HOME` and XDG config/data/state roots, then initialize
its config inside that scope. Jobs sharing one daemon and the same
`api_key_env` name intentionally share the most recently synchronized value.

## Compatibility Contract

Output schemas belong to the pinned official Cursor CLI, not this wrapper. They
can change in a future Cursor release. This repository guards the documented
semantic contract with real official-CLI E2E on isolated hosts and records the
exact accepted versions in [compatibility.md](compatibility.md). An unlisted
version warns at startup and should pass that gate before it is added to the
verified manifest.
