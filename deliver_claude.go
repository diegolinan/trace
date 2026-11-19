package main

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Claude Code deliverer (SPEC A6): a live session's native inbox. Claude Code registers each live session in
// <config>/sessions/<pid>.json with its messaging socket (a named pipe on Windows, a Unix socket elsewhere), and
// <pid>.<sha256 of the socket path>.key holds the peer token its inbox asks for. A writer sends an auth line, then a
// user line wrapped in the cross-session message envelope. These are Claude Code internals, not a published interface
// (read from the bridge-claude-codex plugin, tested live on 2026-11-16): an update can break them, and trace stats
// then shows the failure (A7).
var claudeCode = deliverer{name: claudeName, sessionVar: "CLAUDE_CODE_SESSION_ID", live: claudeLive}

const claudeName = "claude-code"

// claudeEntry is the part of a registry entry the deliverer reads.
type claudeEntry struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
	Socket     string `json:"messagingSocketPath"`
	ProcStart  string `json:"procStart"`
	UpdatedAt  int64  `json:"updatedAt"` // milliseconds
	StartedAt  int64  `json:"startedAt"`
}

func (e claudeEntry) used() int64 { return max(e.UpdatedAt, e.StartedAt) }

// claudeDial connects to an inbox, and processAlive says whether a pid runs: variables, so tests open no real inbox.
var claudeDial, processAlive = dialInbox, pidAlive

// claudeLive lists the live sessions: the registry's entries whose process runs, one per session, the window used last
// (a session can be open in two). No registry is no live session.
func claudeLive() ([]liveTarget, error) {
	dir := filepath.Join(claudeDir(), "sessions")
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	latest := map[string]claudeEntry{}
	for _, name := range names {
		if e, ok := claudeRead(name); ok && e.used() >= latest[e.SessionID].used() {
			latest[e.SessionID] = e
		}
	}
	var out []liveTarget
	for _, e := range latest {
		out = append(out, liveTarget{via: claudeName, id: e.SessionID, cwd: e.Cwd, program: programEntry(e.Entrypoint),
			used: time.UnixMilli(e.used()), send: func(text string) error { return claudeSend(dir, e, text) }})
	}
	return out, nil
}

// claudeRead reads one registry entry: a live one is its own pid's file, names a session and an inbox, and its process
// runs.
func claudeRead(path string) (claudeEntry, bool) {
	var e claudeEntry
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &e) != nil {
		return e, false
	}
	return e, strconv.Itoa(e.PID)+".json" == filepath.Base(path) && e.SessionID != "" && e.Socket != "" && processAlive(e.PID)
}

var peerToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

// claudeSend writes text into the session's inbox, with the peer key of the session's own process, and waits for the
// inbox to close the connection.
func claudeSend(dir string, e claudeEntry, text string) error {
	var key struct {
		Token       string `json:"peerToken"`
		ProcStart   string `json:"procStart"`
		ProcStartFt string `json:"procStartFt"`
	}
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.%x.key", e.PID, sha256.Sum256([]byte(keySocket(e.Socket))))))
	if err != nil {
		return fmt.Errorf("no inbox key: %w", err)
	}
	json.Unmarshal(b, &key)
	if !peerToken.MatchString(key.Token) || cmp.Or(key.ProcStartFt, key.ProcStart) != e.ProcStart {
		return errors.New("the inbox key does not match the session's process")
	}
	conn, err := claudeDial(e.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": key.Token})
	msg, _ := json.Marshal(map[string]any{"type": "user", "uuid": newUUID(), "priority": "next", "from": alarmFrom, "session_id": e.SessionID,
		"message": map[string]string{"role": "user", "content": claudeEnvelope(text)}})
	// The protocol has no acknowledgement: a complete write is the delivery, as the bridge reports it ("written",
	// not acknowledged). A pipe opened as a file cannot half-close, so Claude Code's inbox keeps the connection
	// open and waiting for it would only time out (live, 2026-11-17); closing it is the end of the message.
	_, err = conn.Write([]byte(string(auth) + "\n" + string(msg) + "\n"))
	return err
}

// keySocket is the socket path as Claude Code hashes it for the key file name: a Windows pipe path lowercased
// (the bridge's canonicalLocalSocket; every live key on 2026-11-17 matched it), a Unix socket path as it is.
func keySocket(path string) string {
	if strings.HasPrefix(path, `\\`) {
		return strings.ToLower(path)
	}
	return path
}

// alarmFrom is the sender the alarm's messages name.
const alarmFrom = "TRACE alarm"

var envelopeEnd = regexp.MustCompile(`(?i)</cross-session-message\b`)

// claudeEnvelope wraps text as a message from another session. from-mode="bypass" as the bridge sends it: without it, a
// session in bypass mode holds the message unseen. The text cannot close the envelope.
func claudeEnvelope(text string) string {
	return `<cross-session-message from-name="` + alarmFrom + `" from-mode="bypass">` + "\n" +
		envelopeEnd.ReplaceAllString(text, `<\/cross-session-message`) + "\n</cross-session-message>"
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
