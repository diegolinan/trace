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
  transcript or from the prompt log, is stored with speaker `program`; every import applies the list again. An entry
  may name a `session` instead of dates: what seems typed by the user in that one session is stored the same way,
  and the user's words in other sessions on the same days stay theirs. Tests: `TestNotTypedByUserPeriod`,
  `TestNotTypedByUserSession`.
- **I11.** A Codex thread a program started (`source` `exec` or `mcp` in its session_meta) stores its user messages
  with speaker `program`, as I9 does for Claude Code. Test: `TestCodexProgramThreadsAreNotTheUsers`.
- **I12.** A Claude Code prompt the user sent by accepting a suggestion the model wrote (`promptSource`
  `suggestion_accepted`) is stored with kind `suggestion`. The start block shows it as one, and it never counts as a
  refusal or rule the user stated. Test: `TestAcceptedSuggestionIsNotTheUsersWords`.

## E. Wrong input says so

- **E1.** A `-projects` that names a file, or a `-history` that names a folder, makes the import fail with exit 1
  and the path in the message, instead of reading nothing in silence. A `-projects` that does not exist is still no
  Claude Code (a Codex-only user). Test: `TestWrongSourcePathsFail`.
- **E2.** A search whose words are all punctuation is "search needs words", as an empty one is. Test:
  `TestSearchNeedsRealWords`.

## T. Tasks

A task is something the user asked to be reminded of or noted for later. The store is the board: one per TRACE
home, shared by every agent that writes to it. The agent records and closes tasks with commands; the user never
sees a board, only an agent that remembers and asks.

- **T1.** `trace task add -text "<what>" [-at "<when>"]` reads what is new first (as search does), then records an
  open task tied to the newest message the user typed (speaker `user`, kind `prompt` or `steering`) in the store:
  the message that asked. It prints the task's id and, with `-at`, the time as understood, with its weekday, so
  the agent can confirm it to the user. `-at` takes `YYYY-MM-DD HH:MM` in local time or RFC 3339; anything else
  is refused and nothing is stored. Tests: `TestTaskAddTiesToAskingMessage`, `TestTaskAddEchoesTheTime`,
  `TestTaskAddRefusesABadTime`.
- **T2.** `trace task done <id>` and `trace task drop <id>` close an open task, recording when and the newest
  message the user typed (the words that closed it). Closing a closed or unknown task fails and changes nothing.
  `trace task list` prints the open tasks, oldest first, each with its id, age, time if any, and the words that
  asked for it. Test: `TestTaskCloseAndList`.
- **T3.** The start block lists the open tasks (at most 5, oldest first; with how many more there are) with the
  words that asked for them, and tells the agent: when the user asks to be reminded of something or to note it
  for later, run `trace task add`; close a task only after asking the user. Test: `TestStartBlockShowsOpenTasks`.
- **T4.** On the first user prompt after a task's time, the prompt hook adds one line naming the task, its time
  and the words that asked for it. Each task is announced once; an announced task stays open until closed. Test:
  `TestDueTaskAnnouncedOnceAtNextPrompt`.

## A. Alarm

The alarm brings a task back at its time even when nobody is typing: at the time, it writes a message into a live
agent session, and that message opens a turn and carries the order. One core serves every program; only the
deliverer, which writes into one program's live session, is per program. Agreed with Diego on 2026-11-16 after two
live tests per program (`system/research/2026-11-16_trace-tasks/clock-prior-art.md`).

- **A1.** One alarm per store. `trace alarm` sleeps until the next open task with a time that has not fired, fires
  it, and rereads the store when a task is added, closed or moved. A second `trace alarm` on the same store exits at
  once and fires nothing. The start hook and `trace task add -at` start the alarm when it is not running; started
  twice in a row, one runs. With no open task left that has a time, the alarm exits. Tests:
  `TestAlarmRunsOnce`, `TestTaskAddStartsAlarm`, `TestAlarmExitsWhenNothingIsDue`.
- **A2.** A task fires once. Firing claims the task (when, deliverer, target session) in one store transaction; a
  fired task is never fired again by any path: the alarm, the prompt hook (T4) or the start block. T4's line counts
  as a fire. Test: `TestTaskFiresOnce`.
- **A3.** Times are stored in UTC with the zone they were given in. A task whose time passed while no alarm ran
  (sleep, reboot, no session) fires once when the alarm next runs, and its message says how late it is. Test:
  `TestLateTaskFiresOnceSayingHowLate`.
- **A4.** The message is an order, not a time. It names the task's id, its time and the words that asked for it,
  and tells the agent: if it is in the middle of something, finish it, then end the reply by telling the user;
  then run `trace task told <id>`; close the task only after asking the user. Test: `TestAlarmMessageIsAnOrder`.
- **A5.** It wakes one session: the live session where the asking message was written; if that one is gone, the
  live session used last whose folder is inside the folder tree this TRACE serves. Never a session outside that
  tree, even if it is the only one open; never more than one. Tests: `TestAlarmWakesAskingSessionElseLastInTree`,
  `TestAlarmNeverWakesOutsideTree`.
- **A6.** Deliverers. Claude Code: the session's native inbox (live-session registry, its peer key, the
  cross-session message envelope). Codex: the `codex app-server` daemon through that daemon's own executable
  (`app-server proxy`), `turn/start` on an idle thread, `turn/steer` on a running turn. Code that knows a program
  lives only in its deliverer. Tests (fake inbox, fake daemon): `TestClaudeDelivererWritesPeerMessage`,
  `TestCodexDelivererStartsIdleSteersBusy`.
- **A7.** When no live session qualifies, or delivery fails, the task stays fired and not told: the start block
  lists it as late and the prompt hook names it on the next prompt. A failed delivery is recorded with its reason,
  and `trace stats` shows each deliverer's last failure, so an update that breaks a deliverer shows up. Test:
  `TestFailedDeliveryFallsBackAndIsReported`.
- **A8.** Told. `trace task told <id>` records that the user was told (when, which session). While a task fired into
  a session is not told, that session's stop hook keeps the turn going and names the task (Claude Code: exit 2 with
  the reason on stderr; Codex: `decision: "block"` with a `reason`). When the hook already kept the turn going once
  (`stop_hook_active`), it lets the turn end, so it cannot loop. Tests: `TestStopHookHoldsUntilTold`,
  `TestStopHookDoesNotLoop`.
- **A9.** What a deliverer sends is not the user's words. Each sent message is recorded (session, time, hash of
  the text); the import stores the matching user message as speaker `program`, kind `alarm`, on both programs
  (Codex records it as a plain user message, with no mark of its own). Test: `TestAlarmMessageStoredAsProgram`.

Not decided yet: a notification to the user's desktop or phone when no session is open; starting an agent
(`claude -p`, `codex exec`) for tasks the agent should do alone; an MCP entry point for adding tasks.

## C. Checks

- **C1.** A check's `run` reaches the shell exactly as written, quotes included (`sh -c` on Unix; on Windows,
  `cmd /S /C` with the command line passed as is, not re-escaped). A quoted path with spaces works. Test:
  `TestCheckCommandAsWritten`.
- **C2.** The checks of one tool call share one 45-second limit, under the hook's 60. When it runs out, the
  running check is stopped with every process it started, and the hook returns within a few seconds of the limit.
  Checks still waiting are reported as not run. Tests: `TestChecksShareOneLimit`, and on Unix
  `TestCheckKillsWhatItStarted`, which checks that a child the check started is gone.

