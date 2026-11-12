# Security

## Reporting a vulnerability

Please report security problems privately, not in a public issue. Use [GitHub's private vulnerability reporting](https://github.com/diegolinan/trace/security/advisories/new).

Include:
- what you found;
- how to reproduce it;
- the version (`git describe --tags`, or the release you installed).

TRACE has a single maintainer, so replies are best effort. A confirmed problem gets fixed in a new release, and is credited to you unless you prefer otherwise.

## Supported versions

Only the latest release gets fixes.

## What TRACE stores, and where

Read this before deciding whether something is a vulnerability.

- **Everything stays local.** TRACE copies Claude Code transcripts verbatim into a local SQLite file, and keeps gzip copies of the raw transcripts next to it (by default in `~/.trace`). Nothing is sent over the network.
- **Secrets in the database are redacted, best effort.** API keys, tokens and private keys that match known patterns are removed before an event is stored. A secret in an unknown format can still be stored.
- **The raw copies are not redacted.** They are byte copies of transcript files that already sit on the same disk. Treat the TRACE folder like `~/.claude` itself.
- **Hooks never block a session.** The hooks run with the user's own permissions, and an error in them never fails the agent's turn.

So these are in scope:
- a way to make TRACE send data off the machine;
- a way to read another user's data;
- a way to run code through a crafted transcript or config file;
- a redaction pattern that misses a common secret format.

A secret stored in the raw copies is expected behavior, not a vulnerability.
