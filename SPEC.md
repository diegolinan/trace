# TRACE behavior spec

Each requirement names the test that proves it. A change in behavior changes this file first, then its test, then
the code.

## G. No git

TRACE does not depend on git, does not need it, and does not care whether it exists. A `.git` is one more file in a
folder. TRACE behaves the same with it or without it.

- **G1.** TRACE never runs git. No source file outside tests starts a `git` process. Test: `TestNoGitProcess`.
- **G2.** With no roles configured, a session's role is the folder where it ran, whether or not a `.git` sits in
  that folder or above it. Test: `TestRoleIsTheFolder`.
- **G3.** A configured role matches by its marker file (and the text it must contain). The marker is looked for in
  the session's folder, then in each folder above it, nearest first. No `.git` is consulted. A `git` key in a role is
  ignored. With no marker found up to the top, the role is the caller's fallback (the folder's own). Test: `TestRoleByMarker`.
- **G4.** The start block of a session that ran in folder F shows the newest other session that ran in F or in a
  folder inside F. It never shows a session from a sibling or a parent folder, even one whose name starts the same
  (`site-old` is not inside `site`). The rules the user set, listed in that block, come from the same sessions. With
  configured roles, the role decides, as before. Test: `TestStartBlockFolderOrInside`.
- **G5.** TRACE does not detect or verify commit ids. A hexadecimal string is text, found by search like any word.
  No `commit` anchor is stored. A `repo` key in config.json and the `-repo` flag are accepted and ignored, so a
  hook written for an older TRACE keeps working. A path anchor is the path as written, with `\` read as `/`, never
  made relative to a repository (rows stored before keep theirs). Test: `TestNoCommitAnchors`. Install reads a hook
  entry's flags as main does, `-repo` included, so an older hook with `-repo` is this home's or another's by its
  `-home`, never an unreadable entry that stops install. Test: `TestHookHomeAcceptsRepo`.
- **G6.** A check runs in the folder where the session ran, not in a repository root. Test:
  `TestChecksRunInSessionFolder`.
- **G7.** What a host itself records about a session, such as the branch name in a Claude Code transcript, is
  stored as data. TRACE never computes it.
- **G8.** The rule card's files are the ones Claude Code says it loaded: the last `instructions` record in the
  session's transcript (CLAUDE.md files, their imports, CLAUDE.local.md, auto memory's MEMORY.md), each read again
  from disk so a change shows, or as recorded when the file is gone. TRACE does not search folders for them or work
  out where auto memory lives. With no such record yet, the card has no files (it can only show too little, never
  another project's). Codex already works this way, from its rollout. Test: `TestClaudeSourcesFromTranscript`.
- **G9.** No source file outside tests looks for a `.git`. Test: `TestNoGitProcess`.

## P. Privacy

- **P1.** No text TRACE writes to its database holds a secret that `redact` recognizes. Events, checks (the
  command and its output), the instruction files it copies, and the rule cards all pass through it. The raw copies
  are byte copies and are not redacted (README). A store made before this rule is redacted once when opened. Tests:
  `TestNoSecretInTheStore`, which looks in every text column of every table, and `TestOldRowsRedacted`.
- **P2.** An event's `redacted` field names every kind of secret removed from it, from its text and from its tool
  input alike, each kind once, in the order `redact` checks them. Test: `TestRedactedNamesEveryKind`.

## S. Start block without hooks

- **S1.** `trace resume` with no `-not` leaves out the session asking. That is the recent session (written in
  the last two minutes) whose last stored event is a tool call running `trace resume`. With none such, it is the
  newest recent session. A second live session in the same folder is not taken for the one asking, and a session
  that just ended stays in the block. Test: `TestResumeLeavesOutTheAsker`.
- **S2.** The search command an agent is told to run works as written in its shell when the binary's path holds a
  space. A path without one is left as is. On Windows the path is the 8.3 short form, which has no space and runs in
  PowerShell, cmd and bash alike. When the volume has no short names, the path is quoted, and the PowerShell form
  (`& "..."`) is given beside it. On Unix it is quoted. Test: `TestShellPath`.

## R. Restore

- **R1.** `trace restore` brings back the prompt log's copy (`raw/.claude/history.jsonl.gz`) with any scope,
  the default empty one included. The prompt log is never read as a transcript. Test: `TestRestorePromptLogAnyScope`.

## I. Import

- **I1.** A Codex rollout outside the scope is left out only while that scope is in force. The mark names the
  scope, and a run with another scope reads the rollout again (an old mark without a scope is read again once).
  Test: `TestCodexScopeMarkFollowsScope`.
- **I2.** `install -host codex` imports with the scope config.json holds after the install: the one it just wrote,
  or the one already there. Never the folder it ran from. Test: `TestCodexInstallKeepsConfigScope`.
- **I3.** When Codex compresses a rollout (`x.jsonl` becomes `x.jsonl.zst`), nothing is stored twice, also for rows
  written before rollouts had ordinals. A store made before this rule is mended once when opened: a row keyed by
  the compressed name takes the plain name's key, and the copy of a row stored twice is removed with its tags.
  Tests: `TestCodexCompressedOldRows`, `TestZstKeysMended`.
- **I4.** A session that ran in a temporary or scratch folder is kept out of recall, the same for Claude Code and
  Codex, on Windows (`...\Temp\...`), Linux (`/tmp/...`) and macOS (`/var/folders/...`). The folder is compared
  by its normalized name. Test: `TestExcludedTempFolders`.

- **I5.** A tool result read in a later import than its call takes the tool name of the call in its own session,
  never another session's that used the same call id, for Claude Code and Codex alike. Test:
  `TestLaterResultNamedFromItsOwnSession`.
- **I6.** When a Codex thread uses one call id for several calls, a result takes the name of the latest of them before
  it, whether it is read in the same import as that call or in a later one. Test: `TestCodexReusedCallIDNamedByLatestCall`.
- **I7.** A Claude Code transcript rewritten in place keeps its old raw copy, set aside as `.replaced-<time>.gz`,
  also when the import that sees the rewrite finds no whole line in it yet. Test:
  `TestRewriteCaughtMidLineKeepsOldRawCopy`.
- **I8.** When the import cannot write a transcript's raw copy, its session names or the rule-file texts it
  loaded, that file's import fails and keeps nothing of it, so the next run reads it again. Tests:
  `TestRawCopyFailureKeepsNothing`, `TestNameStoreFailureKeepsNothing`, `TestRuleTextStoreFailureKeepsNothing`.
- **I9.** A prompt in a Claude Code session a program started (entrypoint `sdk-*` or `mcp`) was written by that
  program, not typed by the user: it is stored with speaker `program`, so it is never shown or re-injected as the
  user's words. A store made before this gets the same correction once. Tests: `TestProgramPromptsAreNotTheUsers`,
  `TestOldWorkerPromptsBecomeProgram`.
- **I10.** `not_typed_by_user` in config.json lists periods, as UTC dates from and to (both included), when someone
  or something other than the user typed in the user's terminal. A prompt of a main session inside one, from a
  transcript or from the prompt log, is stored with speaker `program`; every import applies the list again. Test:
  `TestNotTypedByUserPeriod`.

## E. Wrong input says so

- **E1.** A `-projects` that names a file, or a `-history` that names a folder, makes the import fail with exit 1
  and the path in the message, instead of reading nothing in silence. A `-projects` that does not exist is still no
  Claude Code (a Codex-only user). Test: `TestWrongSourcePathsFail`.
- **E2.** A search whose words are all punctuation is "search needs words", as an empty one is. Test:
  `TestSearchNeedsRealWords`.

## C. Checks

- **C1.** A check's `run` reaches the shell exactly as written, quotes included (`sh -c` on Unix; on Windows,
  `cmd /S /C` with the command line passed as is, not re-escaped). A quoted path with spaces works. Test:
  `TestCheckCommandAsWritten`.
- **C2.** The checks of one tool call share one 45-second limit, under the hook's 60. When it runs out, the
  running check is stopped with every process it started, and the hook returns within a few seconds of the limit.
  Checks still waiting are reported as not run. Tests: `TestChecksShareOneLimit`, and on Unix
  `TestCheckKillsWhatItStarted`, which checks that a child the check started is gone.

