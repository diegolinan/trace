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

## C. Checks

- **C1.** A check's `run` reaches the shell exactly as written, quotes included (`sh -c` on Unix; on Windows,
  `cmd /S /C` with the command line passed as is, not re-escaped). A quoted path with spaces works. Test:
  `TestCheckCommandAsWritten`.
- **C2.** The checks of one tool call share one 45-second limit, under the hook's 60. When it runs out, the
  running check is stopped with every process it started, and the hook returns within a few seconds of the limit.
  Checks still waiting are reported as not run. Tests: `TestChecksShareOneLimit`, and on Unix
  `TestCheckKillsWhatItStarted`, which checks that a child the check started is gone.

## K. Does the rule card help

- **K1.** With `"alternate": true` under `reinject` in config.json (off by default, so no user is in a trial
  without asking), the card is shown only on even days and withheld on odd ones, a day being the number of whole
  UTC days since 1970-01-01, so any record's day and arm follow from its UTC timestamp alone. On a withheld day the
  decision is still made and recorded, with the reason prefixed `withheld-` and nothing shown, so the count of new
  context moves the same way in both arms; the card after a compaction is not printed either. Claude Code's own
  reload of its instruction files after a compaction happens in both arms. Diego, 2026-11-17: "dale, va así" (queue
  Q50). Tests: `TestAlternateDaysWithholdCard`, `TestAlternateDaysWithholdCompactCard`.

