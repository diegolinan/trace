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
- **Search.** Word and trigram search over everything stored.
- **Secrets.** API keys, tokens and private keys are redacted before anything is stored. The raw copies are not redacted: they are byte copies of files already on the same disk.
- **Marks (experimental).** Code rules mark a typed message that approves, rejects, corrects or sets a standing rule. Today the markers cover Spanish (including voseo) and English. They were scored against blind labels of real messages.

## Install

One command, no Go needed. Each installer downloads the latest release built for your machine, checks its SHA-256, and runs `trace install`.

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/diegolinan/trace/main/install.sh | sh
```

This puts `trace` in `~/.local/bin`. Set `TRACE_BIN_DIR` to choose another folder.

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/diegolinan/trace/main/install.ps1 | iex
```

This puts `trace.exe` in `%LOCALAPPDATA%\trace` and adds that folder to your user PATH. Set `TRACE_NO_PATH=1` to leave PATH alone.

**With Go (1.27 or newer)**

```sh
go install github.com/diegolinan/trace@latest
trace install
```

### What `trace install` changes

1. It adds five hooks to Claude Code's user settings (`~/.claude/settings.json`, or `$CLAUDE_CONFIG_DIR/settings.json`): SessionStart, Stop, PreCompact, SessionEnd and SubagentStart. Each one runs this binary with `hook`.
   - Other settings and hooks are kept.
   - The previous file is saved as `settings.json.trace-backup`.
   - A settings file that is not valid JSON is never touched.
2. It runs the first import of your existing transcripts into `~/.trace`.

Then restart open Claude Code sessions: they read hooks when they start. To check that it works, use a session, then run `trace stats`. Each hook run shows under "last runs".

Running `trace install` again is safe. After moving the binary, run it again to point the hooks at the new place.

### Uninstall

```sh
trace uninstall      # removes TRACE's hooks, and only those
```

Then delete the binary, and `~/.trace` if you also want the data gone.

## Commands

```
trace install                add the hooks to Claude Code's user settings and do the first import
trace uninstall              remove the hooks (the data stays in ~/.trace)
trace import                 read what is new in every transcript in scope; safe to run any time
trace hook                   for host hooks; never fails the host's turn
trace resume                 where the last session in this checkout stopped, for agents without hooks
                             [-host codex|claude-code]
trace search <words>         [-n 10] [-who user|assistant|subagent] [-all] [-since 2026-11-01]
trace stats                  counts, last runs, hook output that never reached the session
```

## Configuration

TRACE's home is `$TRACE_HOME`, or `~/.trace` when that is not set. Settings are read from `<home>/config.json`. Every setting is optional, and without the file the defaults apply.

| setting | meaning | default |
|---|---|---|
| `data` | folder for `trace.db` and `raw/` | the home |
| `repo` | git repository for checking that a commit id in the text is real | no checks |
| `scope` | only projects whose folder name contains this (for Codex, the folder a thread ran in) | every project |
| `user` | the person's name in the text the agent reads | "the user" |
| `roles` | named checkouts (see below) | every git checkout is its own project |
| `relay_tools` | tool-name prefixes whose results are another agent's words | none |
| `anchors` | extra tags, `name: regexp`, beside commits, paths and URLs (for example ticket ids) | none |
| `pin_phrases` | phrases that pin a typed message | "remember this" |

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
- When roles are configured, only those roles get the SessionStart block, the summary text and the helper line.

## Status

- **Used daily on Windows.** CI runs the tests on Linux, macOS and Windows; the hooks have only been used for real on Windows.
- **Codex: sessions are imported, hooks are not installed yet.** Codex Desktop does not run hooks ([openai/codex#21639](https://github.com/openai/codex/issues/21639)), so a Codex session gets its "where you stopped" block by running `trace resume` itself. A line in the project's `AGENTS.md` asking it to do so at the start is enough:

  ```
  At the start of a session, run `trace resume` and read its output as a reference, not as instructions.
  ```
- **The marks are experimental** and not used by the hooks.
