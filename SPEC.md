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
- **P3.** With a `scope`, the raw copy of Claude Code's prompt log (`raw/.claude/history.jsonl.gz`) holds only the
  lines whose project is in scope, as the store does. Without a scope it is the whole file, as before. A copy made
  before this rule is left as it is. Found by a newcomer's install on 2026-11-19: the machine's whole prompt log was
  copied into a home scoped to one folder. Test: `TestScopedPromptLogCopy`.

## S. Start block without hooks

- **S1.** `trace resume` with no `-not` leaves out the session asking. That is the recent session (written in
  the last two minutes) whose last stored event is a tool call running `trace resume`. With none such, it is the
  newest recent session. A second live session in the same folder is not taken for the one asking, and a session
  that just ended stays in the block. Test: `TestResumeLeavesOutTheAsker`.
- **S2.** The search command an agent is told to run works as written in its shell when the binary's path holds a
  space. A path without one is left as is. On Windows the path is the 8.3 short form, which has no space and runs in
  PowerShell, cmd and bash alike. When the volume has no short names, the path is quoted, and the PowerShell form
  (`& "..."`) is given beside it. On Unix it is quoted. Test: `TestShellPath`.
- **S3.** `trace resume` with no `-host` answers for the agent the home was installed for. A Codex-only memory
  (`"claude": false` in config.json, which `install -host codex` writes or requires) is `codex`, so the line an older
  `AGENTS.md` block runs keeps working. Any other home is `claude-code`. Before 2026-11-19 the default was always
  `codex`, and a newcomer with Claude Code was told "No earlier Codex session". Test: `TestResumeDefaultsToTheHomesAgent`.
- **S4.** When `trace resume` finds no earlier session because S1 left out the only one, it says so: "No earlier
  ... session in this folder. The newest one, <id>, wrote in the last two minutes, so it was taken as the session
  asking; run it again with `-not x` to see it." A newcomer on 2026-11-19 ran resume right after a test session and
  was told only "No earlier session". Test: `TestResumeSaysWhoWasLeftOut`.
- **S6.** S4's hint to run again with `-not x` is given only when that run would show something. When resume shows
  nothing and gives no such hint while sessions of this host and folder are stored but kept out of recall (I4: they
  ran in a temporary folder), it says how many, why, and that `trace search -all` finds them. A newcomer on
  2026-11-19 tried TRACE in `D:\Temp\...`, had two sessions stored, was told only "No earlier session", and `-not x`
  showed nothing. (A third newcomer test the same day, with `claude -p` sessions in a real folder, got the same bare
  "No earlier session" for another reason: those sessions had no folder stored, fixed by I15.) Test:
  `TestResumeExplainsProgramSessions`.
- **S5.** The block's session line names the branch only when the host recorded one; a session outside a
  repository shows no "branch" at all, never "branch ,". Test: `TestResumeSaysWhoWasLeftOut` (its session has none).

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
- **I16.** In a session a program started (I9), a denied tool call and an answer to a question are the program's,
  like its prompts: they are never shown or counted as refusals or rules the user stated. A newcomer's
  `claude -p` on 2026-11-19 had two permission denials shown as the user's last words. Test:
  `TestProgramDenialIsNotTheUsers`.
- **I15.** A Claude Code session is stored from its first row that is an event (has a uuid), so its folder, branch
  and entrypoint come from a row that carries them. Rows before it that only carry a time, such as the
  `queue-operation` rows a `claude -p` transcript now starts with, do not create it. A newcomer's `claude -p`
  sessions on 2026-11-19 were stored with no folder and the project's role instead of `worker`, so no start block
  and no explanation could find them. Sessions stored before this rule keep what they have. Test:
  `TestSessionFromFirstEventRow`.
- **I14.** A pin ("remember this" and the configured `pin_phrases`) marks only a prompt the user typed. A program's
  prompt (I9, I11) gets none: a newcomer's `claude -p "Remember this: ..."` on 2026-11-19 was pinned as the user's.
  Test: `TestProgramPromptIsNotPinned`.
- **I13.** Claude Code's folder is `$CLAUDE_CONFIG_DIR` when it is set, else `~/.claude`, for everything TRACE reads
  or writes there: the transcripts (`projects/`), the prompt log (`history.jsonl`), the settings file and the live
  sessions. Install and import never disagree on it, so a separate Claude Code profile is stored, and nothing from
  the default profile is read for it. Found by a newcomer's install on 2026-11-19: the hooks went to the profile, the
  import read `~/.claude`. Test: `TestClaudeConfigDirIsReadToo`.

## E. Wrong input says so

- **E1.** A `-projects` that names a file, or a `-history` that names a folder, makes the import fail with exit 1
  and the path in the message, instead of reading nothing in silence. A `-projects` that does not exist is still no
  Claude Code (a Codex-only user). Test: `TestWrongSourcePathsFail`.
- **E2.** A search whose words are all punctuation is "search needs words", as an empty one is. Test:
  `TestSearchNeedsRealWords`.
- **E3.** `trace version` (also `-version` and `--version`) prints `trace <version>` and exits 0, with no home and
  even when config.json is broken. The version is the release tag the release build stamps in; else the module
  version Go records (`go install ...@v0.3.36`); else `dev`, with the commit Go records when there is one. Found
  by a newcomer on 2026-11-19: nothing told which version was installed. Tests: `TestVersionCommand`,
  `TestVersionString`.
- **E4.** Only an import the user asked for prints the "files N, events added N" line: `trace import` (with no
  `-trigger`), `restore` and `install`. The import a command runs first (search, resume, task) and the hooks' imports
  print none; a hook's stderr ends up in the host's transcript. `trace stats` prints an empty value as `-`, never Go's
  `<nil>`, and each table starts with a line naming its columns. A newcomer on 2026-11-19 saw the import line before
  every answer, in the transcript after each hook, `<nil>` in stats, and stats columns with no names. Test:
  `TestQuietSideImportAndStats`.
- **E5.** Flag help names the folders TRACE really reads: `$CLAUDE_CONFIG_DIR` or `~/.claude` (I13). Test:
  `TestFlagHelpNamesClaudeConfigDir`.

## T. Tasks

A task is something the user asked to be reminded of or noted for later. The store is the board: one per TRACE
home, shared by every agent that writes to it. The agent records and closes tasks with commands; the user never
sees a board, only an agent that remembers and asks.

- **T1.** `trace task add -text "<what>" [-at "<when>"]` reads what is new first (as search does), then records an
  open task tied to a pair: the session that ran the command (A5) and the message that asked, the newest message
  the user typed (speaker `user`, kind `prompt` or `steering`) in that session. A message typed in any other
  session is never the ask, even a newer one. A subagent's command names its main session (Claude Code gives a
  subagent the main session's id, checked live 2026-11-17), where the user types; no agent id reaches a command,
  so none is stored. Codex gives its thread id, the id the import gives the thread (checked live 2026-11-17). A
  call whose session is not known, or whose session has no typed message, is tied to no message. It prints the task's id and, with `-at`, the time as understood, with its weekday, so
  the agent can confirm it to the user. `-at` takes `YYYY-MM-DD HH:MM` in the task's zone (A10) or RFC 3339; anything else
  is refused and nothing is stored. With `-self` the agent says it adds the task on its own: no message of the
  user asked for it, so the task is tied to none, and wherever its asking words would show (task list, start
  block, prompt hook, alarm) it says that an agent added it on its own. A task tied to no message without
  `-self` (its session not known, or no typed message in it) says instead that the message that asked for it is not
  known, so it is never passed off as the agent's own. Only stored typed messages are ever quoted
  as the user's words: there is no flag that takes them as text, so no agent can write a quote for the user.
  Tests: `TestTaskAddTiesToAskingMessage`, `TestTaskAddAskedInItsOwnSession`, `TestTaskAddSelfTiesToNoMessage`,
  `TestTaskAddEchoesTheTime`, `TestTaskAddRefusesABadTime`.
- **T2.** `trace task done <id>` and `trace task drop <id>` close an open task, recording when and the newest
  message the user typed in the session that ran the command, by T1's rule (the words that closed it). Closing a closed or unknown task fails and changes nothing.
  `trace task list` prints the open tasks, oldest first, each with its id, age, time if any, and the words that
  asked for it. Test: `TestTaskCloseAndList`.
- **T3.** The start block lists the open tasks (at most 5, oldest first; with how many more there are) with the
  words that asked for them, and tells the agent: when the user asks to be reminded of something or to note it
  for later, run `trace task add`, and add `-self` to a task the agent adds on its own; close a task only after
  asking the user. Test: `TestStartBlockShowsOpenTasks`.
- **T4.** On the first user prompt after a task's time, the prompt hook adds one line naming the task, its time
  and the words that asked for it. Each task is fired once (A2); an announced task stays open until closed, and
  until it is told or closed every other session is shown it once (A7). Test:
  `TestDueTaskAnnouncedOnceAtNextPrompt`.
- **T5.** Conditions of ready, fixed when the task is added. A task can name what it waits on before it can start:
  `task add -after <id>` (another task) and `task add -needs "<what>"` (anything else, in words: a decision of the
  user's, a reply from someone, a file or input not there yet). Both repeat. There is no default condition: a task
  with none is ready. The conditions get one numbering per task, in the order given, fixed and never reused.
  `-after` an unknown or dropped task is refused and nothing is stored; `-after` a task already done is stored as
  met. An `-after` condition is met when its task is closed as done; dropped, it is not. `-needs` text is stored and
  shown like a task's text: redacted (P1), clipped to 300 characters on one line, and quoted inside the alarm's
  wrapper (A4). Tests: `TestTaskAddConditions`, `TestTaskAddAfterRefusedOrMet`.
- **T6.** Meeting a condition. `trace task met <id> <n>` meets condition `n` of an open task: a `-needs`, or an
  `-after` whose task was dropped (the user waives it). The agent runs it only after the user says so, as for
  closing (T2), and it records when, the session and the user's newest typed message in it (T1's rule). An unknown
  task or `n`, a closed task, a condition already met, and an `-after` whose task is still open are refused and
  change nothing. `task drop` names the open tasks that wait on the dropped one. Tests: `TestNeedsMetRecordsTheAsk`,
  `TestTaskMetRefusals`, `TestDroppedAfterNamedAndWaivable`.
- **T7.** Showing what a task waits on. `task list`, the start block, the prompt hook's lines and the alarm's
  message (A4) show after a task with unmet conditions `waiting on:` and each unmet one, at most three with "and N
  more": `need 2: "<words>"`, `after task 3 (open)`, `after task 3 (open, itself waiting)`, `after task 3
  (dropped)`. A task with a time still fires when it is waiting (A2), and its message says what it waits on, so the
  agent tells the user the time came and what is missing. `trace task start <id>` checks a task before work on it
  starts: it prints `task <id> is ready` and exits 0, or prints what it waits on and exits 3; it records nothing. An
  unknown or closed task fails with exit 1. The start block tells the agent about `-after`, `-needs`, `task met` and
  `task start`. Tests: `TestWaitingOnShownEverywhere`, `TestAlarmMessageSaysWaitingOn`, `TestTaskStartChecks`.

- **T8.** Conditions of done (Gherkin). `task add -then "<criterion>"` (repeats) and `trace task then <id>
  "<criterion>"` on an open task add a criterion of done: how to tell the task is done. Criteria share the task's
  numbering with its conditions of ready (T5): at add they come after the `-after` and `-needs` ones, in the order
  given; `task then` takes the next number. Numbers are never reused. The text is free and stored like `-needs`
  text (redacted, clipped and quoted when shown); TRACE does not check its form, and the start block asks the agent
  to write each one as Given / When / Then in the user's language. An empty criterion, and `task then` on an
  unknown or closed task, are refused and nothing is stored. A criterion is met only by `task met <id> <n>`, after
  the user says so (T6). Criteria are not conditions of ready: they never show as `waiting on:` and never make a
  task not ready. Tests: `TestThenCriteriaStored`, `TestTaskThenRefusals`.
- **T9.** Closing with criteria. `task done <id>` refuses while a criterion of the task is unmet: it prints each
  unmet one as `then <n>: "<words>"`, exits 1 and changes nothing. `task drop` closes a task whatever its criteria.
  `task start` prints, after its verdict, the task's unmet criteria as `done when: then <n>: "<words>"; ...`, so
  the agent knows what done means before it starts. The start block tells the agent about `-then`, `task then` and
  that `task done` needs every criterion met. Tests: `TestDoneRefusedUntilThenMet`, `TestTaskStartShowsDoneWhen`.

Levels and parents are under the hood (T10 to T13). The user never hears of epics, stories, tasks, subtasks, ids
or a board, and is never asked to pick a level or a parent: the agent decides both from what the user says, and
talks to the user only about the things themselves, in the user's words, at a natural moment (when something
ends, when the user changes subject, at the start of a session), never in the middle of work. The agent is the
scrum master: it moves the board as the situation asks (Diego, 2026-11-17: "el agente es el scrum master...
mueve el tablero como quiere dependiendo de la situación. Punto."). What it may not do alone is what T2 and T6
already keep for the user: closing, dropping and meeting. Most conversations put nothing on the board: a question
answered or work finished in the same conversation is not a task. Something goes on it only when it is left for
later (T1) or is work that will outlast the conversation, and it becomes an epic, a story or a sprint only when the
work really has that shape (Diego, 2026-11-17: "no toda charla deriva en un backlog"). The start block says so (test: `TestStartBlockSaysMostTalkIsNoTask`).
- **T10.** Levels. Every task has one level: `epic`, `story`, `task` or `subtask`. `task add -level <level>` sets
  it; without `-level` it is `task`, so every task stored before levels existed is a `task`. Any other word is
  refused and nothing is stored. `trace task level <id> <level>` changes an open task's level, refused if that
  breaks T11 (its parent one level up, its children one level down) and recorded like a move (T11). Tests:
  `TestTaskLevels`, `TestTaskRelevel`.
- **T11.** Parents, one level up only. `task add -under <id>` puts the new task under an open task one level above
  it: a story under an epic, a task under a story, a subtask under a task. With `-under` and no `-level`, the new
  task takes the level below its parent. An epic has no parent; a story and a task may have none; a subtask must
  have one. An unknown or closed parent, a parent at the wrong level, and a subtask without `-under` are refused
  and nothing is stored. `trace task under <id> <parent>` moves an open task under another open parent by the same
  rules, and `trace task under <id> none` takes it out of its parent (refused for a subtask). Grouping is the
  agent's own call, so a move asks nothing of the user; it records when and the session that ran it. Tests: `TestTaskUnder`, `TestTaskUnderRefusals`,
  `TestTaskMoveUnder`.
- **T12.** Closing a parent. `task done <id>` and `task drop <id>` refuse while a task directly under it is open:
  they print each open child as `task <n> (<level>): "<words>"`, exit 1 and change nothing. The agent then asks
  the user about each pending thing by what it is, never by its level or id ("quedó pendiente X, ¿lo hacemos
  igual o lo dejamos?"), and closes, drops or moves it as the user says. Nothing is closed, dropped or left
  without its parent silently. Test: `TestParentCloseRefusedWithOpenChildren`.
- **T13.** Showing levels. `task list` prints open tasks as a tree: each top task oldest first, its open children
  indented under it, oldest first, each line with its level after its id. The start block, the prompt hook's lines,
  the alarm's message and `task start` name a task's level and, if it has one, its parent (`story 7, in epic 3`); a task at level `task` with no parent
  is shown as before levels, with neither.
  Ready (T5) is not inherited: a child's conditions are its own, and an open parent never makes a child not ready.
  These lines are for the agent, not to be shown to the user. Criteria of done (T8) may be given at any level.
  The start block tells the agent about `-level`, `-under`, `task under` and `task level`; that it is the
  scrum master and picks levels and groups on
  its own; that a story gets its Given / When / Then criteria, agreed with the user in plain words; and that
  levels, ids and the board are never named to the user. Tests: `TestTaskListTree`,
  `TestLevelShownEverywhere`, `TestStartBlockTellsAboutLevels`.
- **T14.** Kinds. A story has one kind: `story` (default), `bug` (something that does not work as it should; its
  criteria say how to reproduce it: what was expected and what happens), `chore` (technical work with no direct value:
  migrate, update, clean), `spike` (find out something in order to decide; its criterion is the question to answer)
  or `impediment` (something that blocks other items; its criterion is what would unblock them). `task add -kind
  <kind>` sets it and `trace task kind <id> <kind>` changes it on an open story. A kind on any level other than story,
  and any other word, are refused and nothing is stored. The agent picks the kind from how the user talks ("no anda"
  is a bug, "hay que investigar" a spike, "actualizá X" a chore) and never names it to the user. Tests:
  `TestTaskKinds`, `TestTaskKindRefusals`.
- **T15.** Points. A story, bug or chore can carry points: effort, as in Scrum, on the Fibonacci scale 1, 2, 3, 5, 8,
  13. `task add -points <n>` sets them and `trace task points <id> <n>` changes them on an open task. Points on a
  level other than story, on a spike or an impediment (a spike is bounded by its time, `-at`, instead), and any other
  number, are refused and nothing is stored. An epic's points are the sum of its stories'. The agent estimates them
  on its own, relative to stories already closed ("like X, which was a 3"), and never says points to the user: it
  says what the work is like in words that fit the work and the moment (quick, a while, delicate, risky, easy,
  involved), chosen by the agent with no fixed table (Diego, 2026-11-17: "sos un LLM. Te vas a dar cuenta solo"); if
  the user says nothing, the estimate stands. At 13 the agent proposes splitting the story. `trace task sizes` prints the last 20 stories closed as done with points, each with
  its points, kind and how long it took from added to done, for the agent to compare with. Points show with the
  level (`story, 3 points, in epic 1`; `epic, 8 points`). Tests: `TestTaskPoints`, `TestTaskPointsRefusals`,
  `TestEpicPointsSum`, `TestTaskSizes`, `TestStartBlockTellsAboutPoints`.
- **T16.** In progress. An open task is to do or in progress. `trace task start <id>` on a ready task now also puts
  it in progress, with when and the session (amends T7's "it records nothing"; a task not ready still records nothing
  and exits 3), and `trace task stop <id>` puts it back to to do, recorded the same way. Without a sprint (T18) work in
  progress is one story at a time: starting a story while another is in progress is refused, naming it, so the agent
  finishes it, stops it or asks the user first. Tasks and subtasks under the story in progress, and plain tasks with
  no parent (a reminder is not board work), are not limited. `task list`, the start block, the prompt hook's lines and
  the alarm's message show `in progress` in the tag. `task sizes` measures from started to done when the story was
  started, otherwise from added. Tests: `TestTaskStartPutsInProgress`, `TestTaskStop`, `TestOneStoryInProgress`,
  `TestInProgressShown`, `TestSizesFromStart`.
- **T17.** Backlog order. Open tasks under the same parent (or with none) have an order of importance: `task add`
  puts a task last among them and `trace task order <id> <n>` moves it to place n, shifting the rest. `task list`,
  the tree and the start block show them in that order instead of oldest first. The agent orders by what the user
  says matters ("esto primero", "urgente", a date) and never names the order to the user. Tests: `TestTaskOrder`,
  `TestTaskOrderRefusals`, `TestListInOrder`.
- **T18.** Sprints. When the user names a date for a set of things ("para el viernes, X, Y y Z"), the agent opens a
  sprint: `trace task sprint -at "<when>" -text "<goal, the user's words>" <id>...` stores its end, its goal and its
  stories (open stories only; one sprint open at a time), and `trace task sprint add|out <id>` changes them. Inside a
  sprint, its stories are not held to one in progress. The start block shows the sprint: goal, end, stories and points
  done and left. At its end the alarm fires it like a task (A2), and the agent goes over each story left with the
  user: done, back to the backlog, or into a next sprint, recorded with the user's words; `trace task sprint close`
  ends it. With no sprint the board is kanban (T16). Tests: `TestSprintOpen`, `TestSprintRefusals`,
  `TestSprintLiftsWIP`, `TestSprintShown`, `TestSprintEndFires`, `TestSprintClose`.

T19 to T22 were a scrum master: a separate agent that ran the board in the background. Two replays of real sessions
failed it, so it was removed on 2026-11-18 and the working agent runs the board alone (T10 to T18). In test 6 it
caught 65.6% of the deferrals against 77.4% for the working agent alone. In test 6b, with the working agent adding
at once and handing over, it caught more but added too much in 13 of 77 conversations (at most 7 allowed) and was
picked as the more useful board in 54% and 49% of conversations (60% needed)
(`system/research/2026-11-16_trace-tasks/README.md`, "Test 6" and "Test 6b"). The numbers T19 to T22 are not reused.

T23 was a reminder from the hook, on a Claude Code session's second `git commit` with no task in progress
(v0.3.40, 2026-11-20). It was removed the same day, so the number T23 is not reused:
- it made the feature depend on the project using git, which this spec avoids (G1, G9);
- the continuity it aimed at, where the work stood when a session ended, is already what the start block gives.

Diego: "o sea que git es una dependencia", then "todo para joggearte la memoria?", then "si".

- **T24. The board as a page.** `trace task board` writes `board.html` beside the store and opens it in the
  browser. It is a snapshot: run it again to refresh. There is no server and no network, and nothing else leaves the
  store.
  - **It reads as a snapshot:** the header gives the time it was made, with its offset, and says changes are made from
    TRACE. Nothing on the page looks editable: no drag handles, create buttons or checkboxes.
  - **Layout:** one grid of four columns by state, with no row per epic: to do, waiting (an unmet `-needs` or `-after`,
    T7), in progress (T16), and closed in the last 14 days, newest first, each marked done or dropped. Each column
    header gives its count. An epic is context on its stories' cards and a filter, never a card of its own.
  - **Above the grid:**
    - the open sprint's goal and review date, when there is one (T18);
    - a search over the words, the asking words, the criteria and the conditions;
    - an epic filter, with "No epic";
    - one line of counts that also filter: in progress, waiting, no criteria, overdue and not told. A task can be in
      more than one, so the counts are not summed.
  - **A card:** its epic, then its id, kind and points; its words, cut at four lines; its due time; its criteria met
    out of all, or "No criteria" for a story with none; the first condition it waits on and how many more; "Overdue,
    not told" when its time passed and the user was not told (A8).
  - **Opening a card shows its detail**, as a panel at the right:
    - the path from its epic, its id, words, state, kind, points and time with offset (A10);
    - every condition it waits on;
    - its criteria of done (T8): how many are met, each marked met or open, literal; a story with none says so;
    - the user's asking words, cleaned of pasted-content tags, or that an agent added it on its own (T1);
    - its parent, its children and its sprint.
  - **Escaping:** every text is HTML-escaped, so a task's words cannot run as markup. Only `**bold**` and `` `code` ``
    are rendered, on the escaped text.
  - **Style:** PAGE's style guide for Diego's site: paper and ink; cyan for in progress, yellow for waiting, pink for
    overdue and not told, each only as a rule or a small label with its words, never a whole card. Square geometry,
    no card shadows. Die Grotesk and Space Mono are embedded in the page (licenses in `NOTICE`). The scheme follows the
    system until the user picks light or dark with a button in the header; the page remembers the pick in the browser.
    The header, the filters and the column headers stay in view: only the cards scroll.
  - **Phone:** one column at a time, picked by a row of state tabs with counts; it starts on in progress when that has
    a card. A search or a count filter shows its results from every state.
  - **Access:** cards are buttons, the detail is a dialog that Escape closes and that gives focus back, focus is
    visible, and every marker carries words, not only color.
  - **Why:** Diego, 2026-11-20: "Deberíamos tener, para quien lo quiere ver y sabe, un tablero del board...", then, on
    the first version: "Apesta fuerte. En donde esta el gherkin?" and "Se ve como un choclo de cosas". The design is
    PAGE's, at his request. Test: `TestTaskBoardPage`.

Why T5 to T7: 25 of 93 real deferrals waited on another task, a third party, a decision of Diego's or an input not
there yet, over the threshold of 1 in 5 fixed before the run. Conditions of done (Gherkin at close) were disputed in
9 of 93, under the threshold of 1 in 10 by one item, and that count is a floor (ten turns often end before the
work does; `system/research/2026-11-16_trace-tasks/test4-ready-done.md`). B first left them out on the threshold
without asking; Diego decided to build them anyway ("si, construilo igual"), with `task done` refusing until every
criterion is met, free text, and criteria addable later (T8, T9).

Why T10 to T13: the board's four levels, épica → historia → tarea → subtarea, agreed by Diego and B
(`system/research/2026-11-16_trace-tasks/README.md`, "A mini board"). In test 5, 39 of 93 real deferrals belonged to
a group with one shared goal, over the threshold of 1 in 4 fixed before the run; the groups were small, like
stories. The sample was our own short history, made without a board, which cannot show an epic that lasts months;
Diego: "no podemos romper el plan solo porque lo que tenemos PARECIERA no cuadrar basado en casos medio bogus", so
all four levels are built (`system/research/2026-11-16_trace-tasks/test5-groups.md`). Points, the backlog's order
and sprints are not specified yet.

## A. Alarm

The alarm brings a task back at its time even when nobody is typing: at the time, it writes a message into a live
agent session, and that message opens a turn and carries the order. One core serves every program; only the
deliverer, which writes into one program's live session, is per program. Agreed with Diego on 2026-11-16 after two
live tests per program (`system/research/2026-11-16_trace-tasks/clock-prior-art.md`). Built: A1 to A10.
Of A5, a Codex thread counts as attached to a client when the daemon holds it loaded (idle or
running); nothing seen yet tells an attached client apart. Not checked live: how each program stores a stop hook's
reason (A9 matches the marker in every shape listed there).

- **A1.** One alarm per store. `trace alarm` holds an OS file lock kept in the store's folder (its own, not the
  import's); a second one exits at once and fires nothing. It wakes at least every 30 seconds, compares the wall
  clock with the due times and rereads the store each time, so laptop sleep, clock changes and tasks added by other
  processes are seen within 30 seconds; it holds no read transaction while it waits. With no open task left that has
  a time still to fire (a fired task never fires again) it releases the lock, reads the store again, and takes the
  lock back if a task appeared meanwhile. The start hook and `trace task add` start it when a task waits to fire and
  it is not running, only where it can detach for real (outside the caller's job, process tree and sandbox); where it
  cannot, it is not started and the start hook is the backstop. It exits when its executable file has changed (size
  or time) or is gone. `trace alarm -stop` asks the running alarm to exit (a stop file it checks at every wake),
  waits up to 45 seconds for its lock, and removes the request either way; the installers run it before replacing
  the binary, which Windows refuses while the alarm runs it. Tests: `TestAlarmRunsOnce`,
  `TestAlarmSeesWallClockJump`, `TestAlarmTakesLockBackForLateTask`, `TestTaskAddStartsAlarm`,
  `TestTaskAddSaysWhenAlarmFails`, `TestStartAlarmRunsAlarm`, `TestStartAloneRefusedInsideJob` (Windows), `TestAlarmExitsWhenReplaced`, `TestAlarmStopsAtTickCap`,
  `TestAlarmStopWaitsForExit`.
- **A2.** A task fires once. The claim is `announced_at`, the column T4 already sets, so an older binary sees a
  fired task as announced; one store transaction claims the task with its target session and state `delivering`,
  and the result (delivered, or failed with its reason) is recorded after. A claim left `delivering` by a crash is
  recorded as failed (crashed) by the next alarm. Delivery is never retried. T4's line is a fire into the session
  that prompted. Tasks announced before the alarm existed count as told. A fire with no qualifying session (A5) is
  recorded failed with why; a failure a deliverer gave starts with its name (`codex: ...`). Tests:
  `TestTaskFiresOnce`, `TestCrashedClaimIsRecordedFailed`, `TestOldAnnouncedTasksCountAsTold`, `TestAlarmStoreErrorsAreReturned`.
- **A3.** Times are stored as a UTC instant with the UTC offset they were said in (A10). A local time that does not
  exist in the zone it is read in (the hour a daylight-saving change skips) is refused and nothing is stored. A
  local time that happens twice (the hour a change repeats) is the earlier instant, and the echo says the hour
  repeats and that the later one is given in RFC 3339 with its offset. A task whose time passed while no alarm ran
  (sleep, reboot, no session) fires once when the alarm next runs, and its message says how late it is, to the
  minute, after its time ("..., 3h12m late."), when it fires 2 minutes or more after it; the alarm's own round of
  checks makes an on-time fire up to a minute late, which it does not mention. Tests:
  `TestLateTaskFiresOnceSayingHowLate`, `TestNonexistentLocalTimeRefused`, `TestRepeatedLocalTimeTakesEarlier`.
- **A4.** The message is an order, not a time, and only its fixed wrapper is the order: if you are in the middle of
  something, finish it, then end the reply by telling the user; then run `trace task told <id>`; close the task
  only after asking the user. The task's text and the words that asked for it are quoted as what to report to the
  user, never as instructions to carry out (any agent in the tree can write a task's text). A task no message
  asked for (T1 `-self`) has only its text quoted, and the wrapper says an agent added it on its own. Every
  message carries the marker `trace-alarm:<id>:<nonce>`. Tests: `TestAlarmMessageIsAnOrderQuotingTheTask`,
  `TestAlarmMessageForSelfAddedTask`.
- **A5.** It wakes one session. `task add` records the session that ran it (the session id the program gives the
  command when it gives one, otherwise the S1 rule); the words that asked come from that session (T1). A session qualifies only
  if it is a main, user-facing session: live, inside the folder tree this TRACE serves, not started by a program
  (I9, I11), not in an excluded folder (I4), with a role this memory speaks to, and for Codex a thread a client is
  attached to. Live means: Claude Code, its registry entry's process is alive; Codex, the daemon lists the thread
  as loaded. The alarm wakes the recorded session if it qualifies, otherwise the qualifying session used last;
  never one outside the tree, even if it is the only one open; never more than one. Tests:
  `TestAlarmWakesSessionThatAddedTask`, `TestAlarmSkipsProgramAndExcludedSessions`,
  `TestAlarmNeverWakesOutsideTree`.
- **A6.** Deliverers. Claude Code: the session's native inbox (live-session registry, its peer key, the
  cross-session message envelope). Codex: the `codex app-server` daemon through that daemon's own executable
  (`app-server proxy`), `turn/start` on an idle thread, `turn/steer` on a running turn; it never starts the
  daemon (not running is a failed delivery, A7), and it considers only threads whose folder is in scope, compared
  the way the import compares folders. Code that knows a program lives only in its deliverer. Tests (fake inbox,
  fake daemon): `TestClaudeDelivererWritesPeerMessage`, `TestCodexDelivererStartsIdleSteersBusy`,
  `TestCodexDelivererNeverStartsDaemon`.
- **A7.** A task fired and not told, for any reason (no qualifying session, a failed delivery, a crash, or the
  target session closed before telling), is listed as late in every start block until told or closed, and the
  prompt hook names it once per session; naming it is not a fire. The session the alarm delivered it into already
  has it in the alarm's message, so the prompt hook never names it there. A failed delivery is recorded with its reason,
  and `trace stats` shows each deliverer's last failure, so an update that breaks a deliverer shows up. Tests:
  `TestFiredUntoldTaskNamedOncePerSession` (the late list and the naming, built), `TestDeliveredSessionNotNamedAgain`,
  `TestUntoldTaskShowsLateAndFailureIsReported`.
- **A8.** Told. `trace task told <id>` records that the user was told and when; the session is the one the claim
  targeted. `task done` and `task drop` also count as told. While a task delivered into a session is not told,
  that session's stop hook keeps the turn going and names the task (Claude Code: exit 2 with the reason on stderr;
  Codex: `decision: "block"` with a `reason`). Only the main session's stop event holds: never a subagent's stop
  or a payload carrying an `agent_id`. When the hook already held the turn (`stop_hook_active`) it lets the turn
  end, and after two holds for one task it stops holding and leaves the task to A7. The hook waits for the store at
  most one second; on any store error it lets the turn end. Tests: `TestStopHookHoldsUntilTold`,
  `TestStopHookDoesNotLoop`, `TestStopHookIgnoresSubagents`, `TestStopHookGivesUpAfterTwoHolds`.
- **A9.** What the alarm or its stop hook writes is not the user's words. The deliverer records each message
  (task, session, marker) before sending it, and every import stores any row carrying an alarm marker, in whatever
  shape the program stored it (a user message, a meta or queued message, a Codex user message), as speaker
  `program`, kind `alarm`. Stop-hook reasons carry the marker too and are stored the same way. Tests:
  `TestAlarmMessageStoredAsProgramInEveryShape`, `TestStopHookReasonIsNotTheUsers`.

- **A10.** Zones. Only the UTC offset matters, never a place: a task stores its UTC instant and the offset it was
  said in (`UTC-03:00`), and every time shown carries its offset. The time is read in, in order: `task add -zone`,
  which the agent passes when the user has said their zone; otherwise the computer's clock, with its daylight-saving
  rules. `-zone` takes an offset (`UTC`, `UTC-3`, `UTC+5:30`) or a zone name (`America/Los_Angeles`), from which
  TRACE takes the offset at that date, daylight saving included; the zone database is built into trace. Refused,
  with nothing stored: an empty zone, `Local`, any `Etc/` name (`Etc/GMT-3` means UTC+3), a name or offset TRACE
  cannot read, and `-zone` without `-at`. With `-at` in RFC 3339 the offset fixes the instant; with `-zone` too, an
  offset that differs from the zone's at that instant is refused. TRACE never looks the zone up on the internet. A
  user who travels does one of two things, and going and coming back are the same case: turns on the system's
  automatic time zone (Windows: Settings > Time & language > Date & time > Set time zone automatically; macOS:
  System Settings > General > Date & Time > Set time zone automatically using your current location; GNOME:
  Settings > Date & Time > Automatic Time Zone), or tells the agent their zone. The README states this as a
  requirement, and says that a server or container's clock is not the user's zone. A one-time task is a fixed
  instant: 11:00 said at UTC-3 fires at 14:00 UTC wherever the user is then. (A time that follows the user matters
  only for repeating tasks, which do not exist yet; it comes with them.) The echo of `task add` shows the time in
  its offset, the time it is now in that offset, and how far away it is: "Tuesday 2026-11-18 11:00 (UTC-03:00);
  now Monday 23:00, in 12h", so a wrong day or zone is seen when the task is added; when the computer's offset at
  that instant differs, it adds the computer's time too: "here 06:00 (UTC-08:00)". `task list`, the start block,
  the prompt hook and the alarm's message show a task's time in its stored offset; a task stored before offsets
  were kept is shown in the computer's offset. The start block tells the agent that `-at` is read in the computer's
  zone, names its offset, says to pass `-zone` when the user gives another zone, and to tell the user the echoed
  time. Diego, 2026-11-17, replacing the lookup by IP he had suggested: "tampoco podemos resolver cada fucking
  problema autoMAGICAmente para todo el mundo y correr el riesgo de buscar la hora en internet si no es seguro...
  Activa tu mierda para que chequee la hora si sos de esos que viajan siempre", "podemos dar las dos opciones 1)
  cambiar la config del reloj 2) avisarle al agente", "ir y volver son lo mismo", and on names: "prefiero UTC-3...
  en DONDE estas fisicamente importa poco, lo que importa es la zona". Prior art:
  `system/research/2026-11-16_trace-tasks/travel-zones-prior-art.md`. Tests:
  `TestOneTimeTaskKeepsItsInstantAcrossZones`, `TestTaskAddZone`, `TestTaskAddRefusesBadZone`,
  `TestRFC3339OffsetMustMatchZone`, `TestTaskAddEchoesNowAndDistance`, `TestTimesShownWithOffset`,
  `TestStartBlockNamesTheZone`, `TestNoZoneLookupOnline`.

Not decided yet: repeating tasks, and with them times that follow the user (A10); a notification to the user's desktop or phone when no session is open; starting an agent
(`claude -p`, `codex exec`) for tasks the agent should do alone; an MCP entry point for adding tasks.

## D. The user's rules and decisions, recorded by the agent

The agent, which understands what the user meant, records each rule or decision the user gives. TRACE ties the record to the user's own message and shows his words, never the agent's. This replaces picking them by word patterns. On 1300 real messages, with a blind judge from another vendor, the word patterns had a precision of 0.29 and a recall of about 0.04. Agents under the instruction of D9 reached 0.72 and about 0.56 (`system/research/2026-11-21_rules-block`). Diego, 2026-11-21: "vamos con b y las dos", then "yes!!!" to the changes a blind review of the design asked for.

- **D1. Recording.** `trace rule add -quote "<his words>" [-decision] [-replaces <id>]` records a rule, or a decision with `-decision`.
  - **The message:** the newest message the user typed in the session that runs the command (prompt, steering, refusal or answer; never a program's or an accepted suggestion, I9, I12, I16) whose text contains the quote.
    - The quote must have at least 3 words.
    - Accents and case do not count when matching.
  - **What is stored:** the message's event id, the quote as matched in the message, the kind, the session and the time.
  - **What is shown** is always the user's text: the quote, with the message clipped around it as context.
  - **It refuses and stores nothing when:**
    - the quote is missing or too short;
    - no message matches;
    - the message is already recorded in force;
    - `-replaces` names an id that is not in force.

    When no message matches, it prints the start of the user's last 3 messages.
  - **On success** it prints `rule <id> recorded: "<quote>"`, and `replaces <id>` when it replaced one.
- **D2. Out of force.**
  - `-replaces <old>` puts the old one out of force, pointing to the new one.
  - `trace rule drop <id>` puts one out of force with nothing replacing it: the user lifted it.
  - Nothing is deleted.
  - A record whose message later stops being the user's (I9, I10) is never shown.
- **D3. Listing.** `trace rule list` prints the records in force, newest first: id, the local time the user said it, kind and quote. `-all` adds those out of force, each with what replaced it or that it was dropped, and when.
- **D4. In the start block and the card.** The block opens a section "What <user> set as rules and decisions (recorded; in force until replaced; what <user> says now wins)".
  - **Which:**
    - rules in force, newest first, up to 8;
    - then decisions recorded in the last 14 days, up to 5.
  - **Each** shows when the user said it (not when it was recorded), its id, its quote, and the end of the reply it answered.
  - **Then** "and N more: trace rule list" when there are more.
  - **Scope:** records from sessions of any host, in the session's folder, in a folder inside it or in a folder above it. With roles, of the same role.
  - **Where:**
    - The section comes before the rest of the block.
    - It is shown even when there is no earlier session to resume.
    - In the card it comes before the files, so the card's budget never cuts it.
  - **Until a store has its first record**, the word-pattern section it replaces is shown instead.
- **D5. A change reaches open sessions.** When a record is added or put out of force, the next card to a live session names it: "out of force: <id>" for one that left.
- **D6. Net at the next prompt.** When the user has typed 10 or more messages in this session since the later of the last record made in it and the last time this net was shown, the prompt hook adds to the agent's context, without showing anything to the user:
  - "Did <user> set a rule or decision in these messages that you have not recorded? Record it with trace rule add, or go on.";
  - those messages, verbatim and clipped.

  UserPromptSubmit is installed by default for this, not only with `reinject`. A subagent never gets the net.
- **D7. Net at compaction.** The block printed after a compaction carries the same question with the user's messages since the last record in this session, whatever the reinject setting.
- **D8.** Retired before it was built: a Rules view on the board page. A blind review of the design found that `trace rule list -all` already covers it (Diego: "si el revisor dice que está mal, sacala"). The number is not reused.
- **D9. What the agent is told** (start block guidance and card):
  - "When <user> gives you a rule or a decision, record it with trace rule add -quote "<his words>" [-decision], then tell him in one line what you recorded."
  - "A rule is how you must work from now on, beyond the task at hand; a decision is a choice about the work that later sessions must keep."
  - "Do not record a hold for the moment, an approval or go for one step, a request for one task, a question, a complaint, an opinion, or a restatement of one already recorded."
  - "When he reverses or replaces one, record the new one with -replaces <id>; when he lifts one, trace rule drop <id>."
  - "When it is about certain files or commands, add -about "<regexp>" (for example -about "publish\.sh|git push"): it then comes back right before such a tool call." (D11)
  - A subagent is told not to record.
- **D10. Hosts.** Codex Desktop runs no hooks, so its sessions get no block and no nets; there, `trace rule list` is the way to see them.
- **D11. A rule at the moment of the action.** `rule add -about "<regexp>"` ties a record to the files or commands it is about, for example `-about "publish\.sh|git push"`.
  - **When:** before a Claude Code tool call whose file or command (as a check's `match` sees it) the regexp matches, without case, the agent gets each matching record in force again, verbatim, in the same line the block uses, under "What <user> said about what you are about to do:". Subagents too: the rule holds for them.
  - **Kept:** a record that replaces another without `-about` keeps the replaced one's. `trace rule list` shows it.
  - **Refused:** an `-about` that does not compile, with nothing stored.
  - Found 2026-11-21 (Diego: "hagalas!!!"). Not measured: whether a rule shown at the action is broken less is open; the checks log is the baseline. Test: `TestRuleAboutAtTheAction`.

## C. Checks

- **C1.** A check's `run` reaches the shell exactly as written, quotes included (`sh -c` on Unix; on Windows,
  `cmd /S /C` with the command line passed as is, not re-escaped). A quoted path with spaces works. Test:
  `TestCheckCommandAsWritten`.
- **C2.** The checks of one tool call share one 45-second limit, under the hook's 60. When it runs out, the
  running check is stopped with every process it started, and the hook returns within a few seconds of the limit.
  Checks still waiting are reported as not run. Tests: `TestChecksShareOneLimit`, and on Unix
  `TestCheckKillsWhatItStarted`, which checks that a child the check started is gone.
- **C3.** A check with `"before": true` runs before the tool call it matches instead of after it, on Claude Code's
  PreToolUse. A non-zero exit stops the call: the tool does not run, and the agent gets the check's name and output
  as the reason. A check that does not compile, or that the limit cuts, lets the call through and says so (a hook
  never blocks by failing). Every run is logged with the checks after. Only Claude Code: TRACE installs no
  PreToolUse on Codex. Found 2026-11-21: a check after the call reports a rule already broken (Diego: "hagalas!!!").
  Test: `TestCheckBeforeStopsTheCall`.
- **C4.** Every check gets `TRACE_TOOL` (the tool's name) and `TRACE_TARGET` (the file it writes or the command it
  runs, as `match` sees it) in its environment, so a check before the call can look at what is about to happen.
  Test: `TestCheckBeforeStopsTheCall`.
- **C5.** Claude Code's PreToolUse is always installed, on the tools that change something (`Bash`, `Edit`,
  `Write`, `MultiEdit`, `NotebookEdit`) and on those the checks before name (`*` when one names none), for C3 and
  D11. Test: `TestInstallPreToolUse`.

## K. Does the rule card help

- **K1.** With `"alternate": true` under `reinject` in config.json (off by default, so no user is in a trial
  without asking), the card is shown only on even days and withheld on odd ones, a day being the number of whole
  UTC days since 1970-01-01, so any record's day and arm follow from its UTC timestamp alone. On a withheld day the
  decision is still made and recorded, with the reason prefixed `withheld-` and nothing shown, so the count of new
  context moves the same way in both arms; the card after a compaction is not printed either. Claude Code's own
  reload of its instruction files after a compaction happens in both arms. Diego, 2026-11-17: "dale, va así" (queue
  Q50). Tests: `TestAlternateDaysWithholdCard`, `TestAlternateDaysWithholdCompactCard`.
- **K2. How much context TRACE takes.** Each hook call that writes something to the agent logs, in the store, when,
  the session, the event (with the SessionStart source) and how many characters it wrote. `trace stats` shows, for
  the last 5 sessions that got any: per event, the calls and characters; the total; and the characters since that
  session's last compaction, which is what is still in its context (about 4 characters a token). Hook calls that write
  nothing log nothing. Found 2026-11-21: codegraph's own measurement showed its tool cut tokens but left more text
  resident; TRACE had no number for its own (Diego: "hagalas!!!"). Test: `TestStatsShowsTraceContext`.

## Q. Search by time, and what was said around a hit

Found 2026-11-21: "what did I tell you today at 11:30?" had no answer without words of the topic, since `-since`
took only a day and a hit could not be opened (Diego: "hagalas!!!").

- **Q1.** `-since` and `-until` each take a day (`2026-11-21`), a day and a time (`2026-11-21 11:30`), RFC 3339,
  or a span back from now (`90m`, `3h`, `2d`). A day or a time is read in `-zone` (default the computer's zone).
  `-since` keeps what was said from the start of that day or minute; `-until` keeps what was said up to the end of
  it; a span or an RFC 3339 time is one instant for both. Any other value is refused with the forms it takes, exit 2.
  Test: `TestSearchTimeWindow`.
- **Q2.** With `-since` or `-until` and no words, search lists what was said in that window, oldest first, at most
  `-n`, with the same filters (`-who`, `-all`) and the same line per hit, the text cut to 300 characters. Without
  words and without a window it is still "search needs words". Test: `TestSearchTimeWindow`.
- **Q3.** `trace show <session>#<seq>` (the session as search prints it, 8 characters or more, and the number after
  `#`; `<session> #<seq>` too) prints that event and the 3 before and after it in the same session (`-n` changes the
  3), oldest first, in the default kinds unless `-all`; the hit itself is always printed. Each text is cut to 2,000
  characters. A session prefix that matches none or several sessions is refused, naming them, exit 1. Search's last
  line names the command. Test: `TestShowAroundAHit`.
- **Q4.** Search ranks by how well a hit matches, weighed by its age: the match score is divided by 1 + its age in
  days / 90, so a hit 90 days old needs twice the match of one from today to rank above it, and of two equal matches
  the newer comes first. Idea from Stanford's Generative Agents (recency in retrieval). Not measured on Diego's
  searches: there is no set of searches with known right answers yet. Test: `TestSearchPrefersRecent`.

