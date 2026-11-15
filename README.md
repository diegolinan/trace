<p align="center">
  <img src="assets/banner.jpg" alt="TRACE: Transcript Recall And Continuity Engine. Memory for coding agents, kept by code." width="100%">
</p>

# TRACE

[![CI](https://github.com/diegolinan/trace/actions/workflows/ci.yml/badge.svg)](https://github.com/diegolinan/trace/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/diegolinan/trace.svg)](https://pkg.go.dev/github.com/diegolinan/trace)
[![Go version](https://img.shields.io/github/go-mod/go-version/diegolinan/trace)](go.mod)
[![Release](https://img.shields.io/github/v/tag/diegolinan/trace?label=release)](https://github.com/diegolinan/trace/tags)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey)
![No model in the loop](https://img.shields.io/badge/model%20in%20the%20loop-none-success)
![For Claude Code](https://img.shields.io/badge/for-Claude%20Code-d97757)
![For Codex](https://img.shields.io/badge/for-Codex-black)

**T**ranscript **R**ecall **A**nd **C**ontinuity **E**ngine: memory for coding agents, kept by code.

Author: Diego Liñan. License: Apache 2.0 (`LICENSE`, `NOTICE`).

In neuroscience, a *memory trace* is the physical mark a memory leaves, also called an engram. TRACE keeps that mark for an agent's sessions. When a session ends, is cleared, or loses its context to a compaction or a crash, the next one starts knowing what happened and where the work stopped.

## Why

A coding agent forgets between sessions. Claude Code's own answer is a model-written summary at compaction time, and it loses details:
- the user's exact words;
- commit ids and paths;
- what was decided and what was refused.

Claude Code also deletes transcripts after 30 days.

TRACE takes a different approach:
- **It keeps the record.** Every transcript is copied verbatim into one local SQLite file, by code, with no model in the loop.
- **It shows the facts.** Each new session sees the user's last words and the last four replies, quoted, not paraphrased.

## What it does

- **Codex too.** It imports Codex threads from `~/.codex/sessions` (or `$CODEX_HOME`), including the compressed `.jsonl.zst` ones. Each row is keyed by thread and ordinal, so a revert or a compressed copy is never stored twice. A subagent's copy of its parent's history is skipped, and so are multi-megabyte image and extension blobs. Each session records which agent wrote it, so a Codex session is never shown as a Claude one.
- **Import.** It reads every Claude Code transcript in scope into `trace.db`, incrementally. It also handles:
  - subagents;
  - transcripts that Claude Code rewrites or splices;
  - tool outputs that Claude Code keeps outside the transcript.

  Raw gzip copies of every transcript are kept next to the database.
- **Identity.** A session is known by its id, never by its name. Every name a session is given is kept as a dated label. A session's role comes from files in its own checkout, so moving or renaming a folder does not change it.
- **Hooks.**
  - Stop, PreCompact and SessionEnd: a detached import that returns at once.
  - SessionStart: a block that says where the last session of the same role stopped. It also lists, verbatim, what the user refused or set as a standing rule in the last 14 days, each with the end of the reply it answered, so the agent does not propose it again. These are found by code rules, with no network.
  - PreCompact: also fixed text that tells Claude Code's summarizer to keep quotes, ids and open work.
  - SubagentStart: tells helper agents that they are helpers, not the agent that started them.
  - PostToolUse, only when `checks` are configured: runs the project's checks after the tool calls they name, and shows a failure to the agent at once.
  - UserPromptSubmit and PostToolUse on every tool, only when `reinject` is configured: the rule card (below).
- **Rule card (opt-in, experimental).** Two memory problems, one tool: across sessions, what happened and where the work stopped; within a session, which rules are in force. Agents stop attending to their instructions as context grows, long before it fills: in published measurements a rule fell below 50% compliance once the context was about 7 to 12 times its size. TRACE reads the size of the context from the host's own token counts in the transcript, and each time enough new context has entered (by default 7 times the card's size) it shows the agent a short card of the project's rules again. The card is built by code, never reworded: the instruction files whole when they fit (CLAUDE.md and what it imports, CLAUDE.local.md, auto memory's MEMORY.md), otherwise only the text you mark between `<!-- trace:rules -->` and `<!-- /trace:rules -->`; then what the user refused or set as a rule in the last 14 days, verbatim. When a file changes, only the new lines are shown. Subagents get no card. Whether this lowers violations is not yet measured; the checks log and `trace stats` are there to measure it. `trace rules` shows the card a Claude Code session in the current folder would get (a Codex card depends on the session's rollout).
- **Checks.** A rule the agent has in context still gets broken: agents stop attending to their instructions as a session grows. A rule a command can verify is better checked than repeated. Each check names the tools it follows (for example `Bash`), a pattern for the file or command (for example `git commit`), and a command; a non-zero exit is a violation, and its output reaches the agent before its next step. Every run is logged, passes too, so `trace stats` shows how often each rule breaks.
- **Search.** Word and trigram search over everything stored.
- **Secrets.** API keys, tokens and private keys are redacted before anything is stored. The raw copies are not redacted: they are byte copies of files already on the same disk.
- **Raw copies you can rebuild from.** Each Claude Code transcript, its large tool results and the prompt log are copied into `raw/` as they are imported, so they outlive Claude Code's cleanup (30 days by default). `trace restore` reads them back into the store: into an empty `trace.db` it rebuilds the Claude Code memory; into an existing one it adds only what is missing. Measured on a real store: 1,063 raw copies, 127,040 events, the same count as the live store, in 7 minutes. Roles are worked out again from the folders as they are now, so a session whose checkout moved can get another role. Codex sessions are not copied (Codex keeps its own), so they come back from `~/.codex` with `trace import`.
- **Marks (experimental).** Code rules mark a typed message that approves, rejects, corrects or sets a standing rule. Today the markers cover Spanish (including voseo) and English. They were scored against blind labels of real messages.

## Install

TRACE works with **Claude Code** and the **Codex CLI/TUI**. One command, no Go needed. Each installer first installs the binary (the latest release for your machine, its SHA-256 checked), then the integration for the agent you name in `TRACE_HOST`: `claude-code` or `codex`. Without it, nothing is downloaded. For `codex`, run it from the project folder and also set `TRACE_CODEX_MODE` to `hooks` or `agents` (see below).

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/diegolinan/trace/main/install.sh | TRACE_HOST=claude-code sh
```

This puts `trace` in `~/.local/bin`. Set `TRACE_BIN_DIR` to choose another folder.

**Windows (PowerShell)**

```powershell
$env:TRACE_HOST = 'claude-code'; irm https://raw.githubusercontent.com/diegolinan/trace/main/install.ps1 | iex
```

This puts `trace.exe` in `%LOCALAPPDATA%	race` and adds that folder to your user PATH. Set `TRACE_NO_PATH=1` to leave PATH alone.

An existing binary that differs from the release is replaced only with `TRACE_UPDATE=1`. If the integration fails after the binary is in place, the installer says so and exits with an error.

**With Go (1.27 or newer)**

```sh
go install github.com/diegolinan/trace@latest
trace install -host claude-code
```

### Where the memory lives

Each memory is one folder, its home: `config.json`, `trace.db` and the raw copies. It is `-home <folder>` on any command, else `$TRACE_HOME`, else `~/.trace`. The installer writes the home's absolute path into what it installs, so the hooks and `AGENTS.md` always open that memory, whatever the folder they run from. Use separate homes for separate memories, for example one for Claude Code and one for a Codex project.

### What `trace install -host claude-code` changes

1. It adds five hooks to Claude Code's user settings (`~/.claude/settings.json`, or `$CLAUDE_CONFIG_DIR/settings.json`): SessionStart, Stop, PreCompact, SessionEnd and SubagentStart. PostToolUse is added when `checks` are configured, limited to the tools they name; with `reinject`, PostToolUse on every tool and UserPromptSubmit (each call takes about 0.1 s). Each one runs this binary with `hook -home <home>`.
   - Other settings and hooks are kept.
   - One TRACE home per settings file. If the file already runs TRACE with another home, nothing is written; uninstall that one first.
   - Hooks from an install made before `-home` existed are taken over only with `-migrate`.
   - The previous file is saved as `settings.json.trace-backup`. A settings file that is not valid JSON is never touched, and one that changes while TRACE writes it is left as the other writer left it.
2. It runs the first import of your existing transcripts into the home. If that fails, the hooks stay installed and the command exits with an error that says how to retry.

The hooks are user-wide, so they fire in every project. Outside the home's `scope` they do nothing: no import, no output. A session that started in scope keeps its memory after it `cd`s elsewhere.

Then restart open Claude Code sessions: they read hooks when they start. To check that it works, use a session, then run `trace stats`. Each hook run shows under "last runs".

Running the same install again changes nothing. After moving the binary, run it again to point the hooks at the new place.

### What `trace install -host codex` changes

Run it from the project folder with one of two modes, one per home; there is no default, because they promise different things:

- `-mode agents` asks Codex, through a block in that folder's `AGENTS.md`, to run `trace resume -home <home>` when a session starts. The block is marked with the home, so a later install or uninstall touches only its own. Codex runs it only because `AGENTS.md` asks; nothing checks that a session did.
- `-mode hooks` registers TRACE's hooks in `$CODEX_HOME/hooks.json` (`~/.codex/hooks.json`), the same functions as for Claude Code, with the same `config.json`:
  - SessionStart, every source: on a new, resumed, cleared or forked session it shows where the last one stopped; after a compaction, with `reinject`, the rule card. Codex runs it when the first prompt is sent, not when the TUI opens.
  - Stop imports each finished turn. Interrupt (Codex only; Claude Code has no such event) imports a turn cut short. SessionEnd imports at a clean shutdown, best effort: Codex gives it 1 s by default and 3 s at most.
  - SubagentStart tells a subagent it is not its parent.
  - PostToolUse and UserPromptSubmit, as for Claude Code, only with `checks` or `reinject`. The card's instructions are the ones Codex itself loaded (its `AGENTS.md` chain, overrides and byte budget, after its trust check), read from the session's rollout; the context size is Codex's own count for the window, written after each model response, so the card can come one response late.
  - Not possible in Codex: steering the compaction summary (Codex ignores PreCompact output, so PreCompact is not registered), and replacing a tool's output.
  - No hook runs when Codex is killed or crashes, on either host: the last finished turn or interruption is what is stored, and the rest comes in at the next import.
  - The hooks that give context set `additionalContextLimit` to 4,000 tokens, so Codex does not cut them at its 2,500-token default.
  - They fire in every project, and outside the home's `scope` they do nothing; each call still starts trace (and, on Windows, PowerShell) before that check, so with `checks` or `reinject` every tool call of every Codex session pays it. Codex runs a hook only after you review and trust it in Codex; the installer never trusts it for you. On Windows the hook starts PowerShell with the call encoded, because Codex may run hooks through cmd or PowerShell; the installer prints the decoded script. That adds about 260 ms per call on the Windows machine it was measured on, and a failing exit code may arrive as 1 rather than its own value. Elsewhere the command is quoted for sh, bash and zsh (not fish).

Both modes:

- If the home has no `config.json`, it writes one that reads no Claude Code transcripts (`"claude": false`) and keeps the threads that ran in this folder. An existing config is used only if it already does that; a Claude Code memory is never turned into a Codex one.
- Everything is checked before the first write: the other mode already installed for this home, another home's block or hooks, a `trace resume` request outside a block, or an older install's line (taken over only with `-migrate`) stop it. If the second write fails, the config it just wrote is removed again while it is still what it wrote.

Status of hooks mode, on Windows: under Codex 0.169.0 (app server and `codex exec`, with a local test model server and throwaway homes), the hooks stay untrusted and do not run until trusted; once trusted, SessionStart and Stop run on a new session and on a resumed one after a restart, and the block reaches the request Codex sends to the model. The commands also ran through cmd and PowerShell 5.1 with paths holding spaces, an apostrophe and ñ. The other events (Interrupt, SessionEnd, SubagentStart, PostToolUse, UserPromptSubmit, SessionStart after clear and compact) are built on payloads captured from Codex 0.169.0 and on its source, and tested against the output Codex accepts; they then ran under Codex 0.169.0 with TRACE as the hook, in a throwaway profile with a local test model, and every event completed. In real use (Windows, Codex 0.169.0 engine behind a 0.167.2 TUI, a real model), a phrase sent in one session was stored at Stop and, after `/clear`, the new session's model gave it back with no search and no tool call. Reported checked by the author on Windows (2026-11-14): Ctrl-C and /quit in the TUI, shell and patch tool calls, other Codex versions and PowerShell 7. Not checked yet: macOS and Linux, and a compaction with a real model.

A Codex thread reads `hooks.json` when it starts, so hooks installed while a session is open do not show in its `/hooks` until it is reopened (`/quit`, then resume, which then offers the new hooks for review).

### Uninstall

```sh
trace uninstall -host claude-code   # removes this home's hooks, and only those
trace uninstall -host codex -mode hooks|agents       # removes this home's hooks from hooks.json and its block from AGENTS.md in the current folder
```

The data stays in the home. Then delete the binary, and the home if you also want the data gone.

## Commands

```
trace install -host claude-code   add the hooks to Claude Code's user settings and do the first import
trace install -host codex -mode hooks|agents   a Codex memory for the current folder (no default mode)
trace uninstall -host <agent>     remove what install added for this home (the data stays)
trace import                      read what is new in every transcript in scope; safe to run any time
trace restore                     rebuild the store from the raw copies (a lost or broken trace.db, or transcripts the host deleted); skips what is stored
trace hook                        for host hooks; never fails the host's turn
trace resume                      where the last session in this checkout stopped, for agents without hooks
                                  [-host codex|claude-code]
trace search <words>              [-n 10] [-who user|assistant|subagent] [-all] [-since 2026-11-01]
trace rules                       the rule card a session in this folder would get, and why
trace stats                       counts, last runs, checks, rule cards, hook output that never reached the session

-home <folder>                    on any command: the memory to use
-migrate                          install, uninstall: take over what an install before -home wrote
```

## Configuration

TRACE's home is `-home`, else `$TRACE_HOME`, else `~/.trace`. Settings are read from `<home>/config.json`. Every setting is optional, and without the file the defaults apply. A file that is not valid JSON stops every command except `hook`, which does nothing, rather than run on the defaults (the default scope is every project). A UTF-8 BOM is accepted.

| setting | meaning | default |
|---|---|---|
| `data` | folder for `trace.db` and `raw/` | the home |
| `repo` | git repository for checking that a commit id in the text is real | no checks |
| `scope` | a folder, or a list of folders: only sessions that ran in one of them (for Claude Code, its transcript folder name; for Codex, the folder a thread ran in). Matched as a part of the path, without case, with every character other than a letter or digit read as `-`, so a full path, a folder name or `diegolinan.me` all work | every project |
| `claude` | `false`: never read Claude Code transcripts or `~/.claude/history.jsonl` (a Codex-only memory) | `true` |
| `user` | the person's name in the text the agent reads | "the user" |
| `roles` | named checkouts (see below) | every git checkout is its own project |
| `relay_tools` | tool-name prefixes whose results are another agent's words | none |
| `anchors` | extra tags, `name: regexp`, beside commits, paths and URLs (for example ticket ids) | none |
| `pin_phrases` | phrases that pin a typed message | "remember this" |
| `checks` | rules a command can verify, run after matching tool calls (see below) | none |
| `reinject` | turns on the rule card: `{}`, or `{"factor": 7, "budget": 8000}` (new context, in card sizes, before it comes again; most characters of the card) | off |

A role names a checkout by a file in its root, so it survives moves and renames:

```json
{
  "user": "Ana",
  "roles": [
    { "name": "lead", "label": "the lead agent", "git": "dir", "marker": "AGENT.md", "contains": "role: lead", "folder": "my-repo" },
    { "name": "worker", "label": "the worker agent", "git": "file", "marker": "AGENT.md", "contains": "role: worker", "folder": "worktrees-worker" }
  ]
}
```

- `git` is `"dir"` for a main checkout, `"file"` for a linked worktree, or empty for either.
- `folder` is the fallback when the session's folder no longer exists. It is matched against the end of Claude Code's transcript folder name, without case.
- When roles are configured, only those roles get the SessionStart block, the summary text, the helper line and the checks.

A check runs after a tool call when `on` matches the tool's name (a regexp, as in Claude Code's hook matchers; empty: every tool) and `match` matches the file the tool wrote or the command it ran. `run` is a shell command, run from the root of the session's git checkout with a 45-second limit:

```json
{
  "checks": [
    { "name": "changelog", "on": "Bash", "match": "git\\b.*\\bcommit", "run": "node scripts/check-changelog.mjs" },
    { "name": "lint", "on": "Write|Edit", "match": "\\.go$", "run": "go vet ./..." }
  ]
}
```

After adding, changing or removing checks or `reinject`, run `trace install` again (`-host claude-code`, or `-host codex -mode hooks`; Codex then asks you to trust the changed hooks): the hooks only listen to what the configuration needs. On Codex a check's `on` matches Codex's tool names, which are not yet verified for shell commands and patches (a fixture in Codex's source names a shell call `Bash`, with the command in `tool_input`); checks on Codex are built but have not run on a real Codex tool call.

## Status

- **Used daily on Windows.** CI runs the tests on Linux, macOS and Windows; the hooks have only been used for real on Windows.
- **Claude Code and Codex CLI/TUI.** Codex Desktop is not supported. Codex gets its block through `AGENTS.md` or through Codex hooks (see Install); hooks mode is checked on Windows with Codex 0.169.0 only.
- **The marks are experimental** and not used by the hooks.
