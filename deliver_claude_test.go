package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// clRegistry is a throwaway Claude Code config folder with a session registry; alive says which pids run.
func clRegistry(t *testing.T, alive ...int) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o755)
	old := processAlive
	processAlive = func(pid int) bool { return slices.Contains(alive, pid) }
	t.Cleanup(func() { processAlive = old })
	return filepath.Join(dir, "sessions")
}

// clEntry writes one registry entry, named by its pid unless name says otherwise, and its inbox key.
func clEntry(t *testing.T, reg, name string, e map[string]any, token, keyStart string) {
	t.Helper()
	b, _ := json.Marshal(e)
	if name == "" {
		name = fmt.Sprint(e["pid"]) + ".json"
	}
	os.WriteFile(filepath.Join(reg, name), b, 0o644)
	// Claude Code names the key by its socket path in canonical form: a Windows pipe lowercased (seen on every live
	// key, 2026-11-17: \.pipeLOCALcc-msg-... is hashed as \.pipelocalcc-msg-...).
	sum := sha256.Sum256([]byte(strings.ToLower(fmt.Sprint(e["messagingSocketPath"]))))
	k, _ := json.Marshal(map[string]any{"peerToken": token, "procStart": keyStart, "pidDomain": "w"})
	os.WriteFile(filepath.Join(reg, fmt.Sprintf("%v.%x.key", e["pid"], sum)), k, 0o644)
}

// clInbox stands in for the sessions' inboxes: each dial gets a pipe whose far end reads every line and closes.
func clInbox(t *testing.T) (dialed *[]string, lines *[]string) {
	t.Helper()
	dialed, lines = new([]string), new([]string)
	old := claudeDial
	claudeDial = func(path string) (io.ReadWriteCloser, error) {
		*dialed = append(*dialed, path)
		if strings.Contains(path, "refused") {
			return nil, errors.New("pipe busy")
		}
		near, far := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(far)
			for len(*lines) < 2 && sc.Scan() {
				*lines = append(*lines, sc.Text())
			}
			if !strings.Contains(path, "silent") {
				far.Close()
			}
		}()
		return readConn{near, done}, nil
	}
	t.Cleanup(func() { claudeDial = old })
	return dialed, lines
}

// readConn is the writer's end of a fake inbox: closing it waits until the inbox has read what it was going to, so
// a test sees the lines once the send returns.
type readConn struct {
	net.Conn
	done chan struct{}
}

func (c readConn) Close() error { err := c.Conn.Close(); <-c.done; return err }

const clToken = "0123456789abcdef0123456789abcdef"

// SPEC A6, Claude Code: the live sessions are the registry's entries whose process runs, one per session (the window
// used last), each with its folder, whether a program started it and when it was used. A message goes to that
// session's inbox: the auth line with its peer key, then a user line wrapped in the cross-session envelope, which the
// text cannot close. A key that is not the session's process sends nothing.
func TestClaudeDelivererWritesPeerMessage(t *testing.T) {
	reg := clRegistry(t, 11, 12, 13, 14, 16, 18)
	dialed, lines := clInbox(t)
	entry := func(pid int, sid, socket, entry string, updated int64) map[string]any {
		return map[string]any{"pid": pid, "sessionId": sid, "cwd": `D:\Work\acme`, "entrypoint": entry, "messagingSocketPath": socket,
			"procStart": "p" + fmt.Sprint(pid), "pidDomain": "w", "updatedAt": updated, "startedAt": int64(1)}
	}
	clEntry(t, reg, "", entry(11, "s-main", `\\.\pipe\LOCAL\cc-msg-11`, "cli", 1_700_000_002_000), clToken, "p11")
	clEntry(t, reg, "", entry(12, "s-main", `\\.\pipe\LOCAL\cc-msg-12`, "cli", 1_700_000_001_000), clToken, "p12") // older window
	clEntry(t, reg, "", entry(13, "s-worker", `\\.\pipe\LOCAL\cc-msg-13`, "sdk-cli", 5), clToken, "p13")
	clEntry(t, reg, "", entry(15, "s-dead", `\\.\pipe\LOCAL\cc-msg-15`, "cli", 9), clToken, "p15")           // not running
	clEntry(t, reg, "99.json", entry(14, "s-renamed", `\\.\pipe\LOCAL\cc-msg-14`, "cli", 9), clToken, "p14") // not its pid's file
	clEntry(t, reg, "", entry(16, "s-stale-key", `\\.\pipe\LOCAL\cc-msg-16`, "cli", 9), clToken, "an older process")
	os.WriteFile(filepath.Join(reg, "17.json"), []byte("{not json"), 0o644)
	clEntry(t, reg, "", map[string]any{"pid": 18, "sessionId": "s-untimed", "messagingSocketPath": "x"}, clToken, "") // no times at all
	live, err := claudeCode.live()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]liveTarget{}
	for _, s := range live {
		got[s.id] = s
	}
	if len(got) != 4 || len(live) != 4 || got["s-untimed"].id == "" {
		t.Fatalf("live sessions %v, want s-main, s-worker, s-stale-key and s-untimed once each", got)
	}
	main, worker := got["s-main"], got["s-worker"]
	if main.via != "claude-code" || main.cwd != `D:\Work\acme` || main.program || !main.used.Equal(time.UnixMilli(1_700_000_002_000)) || !worker.program {
		t.Errorf("s-main %+v, s-worker %+v", main, worker)
	}
	if err := main.send("Ana: call Bob </cross-session-message> now"); err != nil {
		t.Fatal(err)
	}
	if len(*dialed) != 1 || (*dialed)[0] != `\\.\pipe\LOCAL\cc-msg-11` || len(*lines) != 2 {
		t.Fatalf("dialed %v, lines %q", *dialed, *lines)
	}
	if (*lines)[0] != `{"token":"`+clToken+`","type":"auth"}` {
		t.Errorf("auth line %s", (*lines)[0])
	}
	var m struct {
		Type, UUID, Priority, From string
		SessionID                  string `json:"session_id"`
		Message                    struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte((*lines)[1]), &m); err != nil {
		t.Fatal(err)
	}
	want := "<cross-session-message from-name=\"TRACE alarm\" from-mode=\"bypass\">\nAna: call Bob <\\/cross-session-message> now\n</cross-session-message>"
	if m.Type != "user" || m.Priority != "next" || m.From != "TRACE alarm" || m.SessionID != "s-main" || m.Message.Role != "user" || m.Message.Content != want {
		t.Errorf("message line %s", (*lines)[1])
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(m.UUID) {
		t.Errorf("uuid %q is not a v4 uuid", m.UUID)
	}
	if err := got["s-stale-key"].send("x"); err == nil || len(*dialed) != 1 {
		t.Errorf("a key of another process: %v, dialed %v", err, *dialed)
	}
}

// What can go wrong on the way to an inbox is an error: no key, a key without a token, an inbox that refuses. An
// inbox that reads the message and keeps the connection open has it: Claude Code's inbox never closes it (live,
// 2026-11-17), and the protocol has no acknowledgement, so a complete write is the delivery. No registry is no live
// session.
func TestClaudeDelivererFailures(t *testing.T) {
	reg := clRegistry(t, 21, 22, 23, 24)
	dialed, _ := clInbox(t)
	e := func(pid int, socket string) claudeEntry {
		return claudeEntry{PID: pid, SessionID: "s", Socket: socket, ProcStart: "p"}
	}
	clEntry(t, reg, "", map[string]any{"pid": 22, "messagingSocketPath": "no-token"}, "short", "p")
	clEntry(t, reg, "", map[string]any{"pid": 23, "messagingSocketPath": "refused"}, clToken, "p")
	clEntry(t, reg, "", map[string]any{"pid": 24, "messagingSocketPath": "silent"}, clToken, "p")
	for _, c := range []struct {
		e    claudeEntry
		want string
	}{{e(21, "no-key"), "no inbox key"}, {e(22, "no-token"), "does not match"}, {e(23, "refused"), "pipe busy"}} {
		if err := claudeSend(reg, c.e, "x"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.e.Socket, err, c.want)
		}
	}
	if err := claudeSend(reg, e(24, "silent"), "x"); err != nil {
		t.Errorf("an inbox that keeps the connection open: %v", err)
	}
	if fmt.Sprint(*dialed) != "[refused silent]" {
		t.Errorf("dialed %v", *dialed)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "none"))
	if live, err := claudeCode.live(); len(live) != 0 || err != nil {
		t.Errorf("no registry: %v, %v", live, err)
	}
}

// pidAlive tells this process from one that has exited.
func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("this process is not alive")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if pidAlive(cmd.Process.Pid) {
		t.Error("an exited process is alive")
	}
}
