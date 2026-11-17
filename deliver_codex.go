package main

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The Codex deliverer (SPEC A6): the `codex app-server` daemon, reached through the daemon's own executable
// (<CODEX_HOME>/packages/app-server-daemon/current/bin; the CLI on PATH may be another version, whose proxy timed out
// on 2026-11-16). `codex app-server proxy` carries a WebSocket over its stdio, and JSON-RPC over that: turn/start on an
// idle thread, turn/steer on a running turn. It never starts the daemon: one that is not running is a failed delivery
// (A7). Read from the bridge-claude-codex plugin and tested live on 2026-11-16 with Codex 0.169.0.
var codexDeliverer = deliverer{name: codexName, sessionVar: "CODEX_THREAD_ID", live: codexLive}

const codexName = "codex"

// codexThread is the part of a daemon thread the deliverer reads.
type codexThread struct {
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	Status struct {
		Type string `json:"type"` // notLoaded, idle, active, systemError
	} `json:"status"`
	Source    json.RawMessage `json:"source"`
	UpdatedAt int64           `json:"updatedAt"` // seconds
}

// codexLive lists the threads the daemon holds loaded (idle or running). No daemon installed is no Codex session the
// alarm can reach.
func codexLive() ([]liveTarget, error) {
	exe := codexDaemonExe()
	if _, err := os.Stat(exe); err != nil {
		return nil, nil
	}
	c, err := codexOpen(exe)
	if err != nil {
		return nil, err
	}
	defer c.close()
	// shortcut: one page of the 50 threads updated last; a loaded thread older than those is not seen. Upgrade if one
	// is ever missed.
	var page struct {
		Data []codexThread `json:"data"`
	}
	if err := c.call("thread/list", map[string]any{"limit": 50, "sortKey": "updated_at"}, &page); err != nil {
		return nil, err
	}
	var out []liveTarget
	at := map[string]int{}
	for _, th := range page.Data {
		if th.Status.Type == "idle" || th.Status.Type == "active" {
			src := string(th.Source)
			out = addOnce(out, at, liveTarget{via: codexName, id: th.ID, cwd: th.Cwd, used: time.Unix(th.UpdatedAt, 0),
				program: src == `"exec"` || src == `"mcp"` || strings.Contains(strings.ToLower(src), "subagent"), // as I11, and a subagent's thread
				send:    func(text string) error { return codexSend(exe, th.ID, text) }})
		}
	}
	return out, nil
}

// addOnce adds a thread once: the daemon lists some threads several times (live, 2026-11-17), and the entry used
// last stands for it. at maps an id to its place in out.
func addOnce(out []liveTarget, at map[string]int, t liveTarget) []liveTarget {
	if i, ok := at[t.id]; ok {
		if t.used.After(out[i].used) {
			out[i] = t
		}
		return out
	}
	at[t.id] = len(out)
	return append(out, t)
}

// codexSend writes text into a thread: a new turn when it is idle, into the running turn when one runs.
func codexSend(exe, thread, text string) error {
	c, err := codexOpen(exe)
	if err != nil {
		return err
	}
	defer c.close()
	var read struct {
		Thread codexThread `json:"thread"`
	}
	if err := c.call("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, &read); err != nil {
		return err
	}
	input := []map[string]string{{"type": "text", "text": text}}
	switch read.Thread.Status.Type {
	case "idle":
		return c.call("turn/start", map[string]any{"threadId": thread, "input": input}, nil)
	case "active":
		// The running turn is the newest. shortcut: a steer the daemon refuses because that turn just ended is a failed
		// delivery, not a new turn; upgrade if A7 shows such failures.
		var turns struct {
			Data []struct {
				ID     string          `json:"id"`
				Status json.RawMessage `json:"status"` // "inProgress", or an object with that type
			} `json:"data"`
		}
		if err := c.call("thread/turns/list", map[string]any{"threadId": thread, "limit": 1}, &turns); err != nil {
			return err
		}
		if len(turns.Data) == 0 || !bytes.Contains(turns.Data[0].Status, []byte(`"inProgress"`)) {
			return errors.New("the thread runs a turn the daemon does not list")
		}
		return c.call("turn/steer", map[string]any{"threadId": thread, "expectedTurnId": turns.Data[0].ID, "input": input}, nil)
	}
	return fmt.Errorf("the thread is %s, not idle or running", cmp.Or(read.Thread.Status.Type, "unknown"))
}

// codexDaemonExe is the daemon's own executable.
func codexDaemonExe() string {
	exe := "codex"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return filepath.Join(codexHome(), "packages", "app-server-daemon", "current", "bin", exe)
}

// codexRun starts a program and returns its stdin and stdout as one stream; a variable so tests start no process.
var codexRun = startProxy

// codexWait bounds one connection to the daemon, so a daemon that does not answer never holds the alarm.
var codexWait = 15 * time.Second

// codexConn is one JSON-RPC connection to the daemon, over a WebSocket over the proxy's stdio.
type codexConn struct {
	rw    io.ReadWriteCloser
	br    *bufio.Reader
	id    int
	timer *time.Timer
}

// codexOpen starts the proxy, upgrades its stream to a WebSocket and initializes. The proxy only connects to a
// running daemon; it never starts one.
func codexOpen(exe string) (*codexConn, error) {
	rw, err := codexRun(exe, "app-server", "proxy")
	if err != nil {
		return nil, err
	}
	c := &codexConn{rw: rw, br: bufio.NewReader(rw), timer: time.AfterFunc(codexWait, func() { rw.Close() })}
	if err := c.handshake(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *codexConn) close() {
	c.timer.Stop()
	c.rw.Close()
}

// handshake is the WebSocket upgrade (RFC 6455 4.1), then initialize and its initialized notice.
func (c *codexConn) handshake() error {
	key := make([]byte, 16)
	rand.Read(key)
	fmt.Fprintf(c.rw, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		base64.StdEncoding.EncodeToString(key))
	status, err := c.br.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.Contains(status, " 101 ") {
		return fmt.Errorf("the daemon refused the WebSocket: %s", strings.TrimSpace(status))
	}
	for line := status; strings.TrimSpace(line) != ""; {
		if line, err = c.br.ReadString('\n'); err != nil {
			return err
		}
	}
	if err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "trace", "title": alarmFrom, "version": "1"}}, nil); err != nil {
		return err
	}
	return c.write(map[string]any{"method": "initialized", "params": map[string]any{}})
}

// call sends one request and reads its answer; result, when not nil, gets the answer's result.
func (c *codexConn) call(method string, params, result any) error {
	c.id++
	if err := c.write(map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		return err
	}
	m, err := c.answer()
	switch {
	case err != nil:
		return err
	case m.Error != nil:
		return fmt.Errorf("%s: %s (%d)", method, m.Error.Message, m.Error.Code)
	case result == nil:
		return nil
	}
	return json.Unmarshal(m.Result, result)
}

// rpcMessage is a JSON-RPC message as the daemon sends it: an answer, or a notice or request of its own (a method).
type rpcMessage struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// answer reads until the answer to the last request, skipping the daemon's own notices and requests.
func (c *codexConn) answer() (rpcMessage, error) {
	for {
		b, err := c.next()
		if err != nil {
			return rpcMessage{}, err
		}
		var m rpcMessage
		if json.Unmarshal(b, &m) == nil && m.Method == "" && m.ID != nil && *m.ID == c.id {
			return m, nil
		}
	}
}

func (c *codexConn) write(v any) error {
	b, _ := json.Marshal(v)
	return wsWrite(c.rw, 1, b, true)
}

// next is the next whole text message; it answers pings, and a close is the end.
func (c *codexConn) next() ([]byte, error) {
	var msg []byte
	for {
		fin, op, p, err := wsRead(c.br)
		if err != nil {
			return nil, err
		}
		switch op {
		case 8:
			return nil, errors.New("the daemon closed the connection")
		case 9:
			wsWrite(c.rw, 10, p, true)
			continue
		case 10:
			continue
		}
		msg = append(msg, p...)
		if fin {
			return msg, nil
		}
	}
}

// wsWrite writes one final frame; a client masks what it sends (RFC 6455 5.3).
func wsWrite(w io.Writer, op byte, p []byte, mask bool) error {
	n := len(p)
	h := []byte{0x80 | op, byte(n)} // a length under 126 is the second byte itself
	switch {
	case n >= 1<<16:
		h[1] = 127
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	case n >= 126:
		h[1] = 126
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	}
	body := bytes.Clone(p)
	if mask {
		k := make([]byte, 4)
		rand.Read(k)
		h[1] |= 0x80
		h = append(h, k...)
		for i := range body {
			body[i] ^= k[i%4]
		}
	}
	_, err := w.Write(append(h, body...))
	return err
}

// wsRead reads one frame: whether it ends its message, its opcode and its payload, unmasked.
func wsRead(r *bufio.Reader) (fin bool, op byte, p []byte, err error) {
	h := make([]byte, 2)
	if _, err = io.ReadFull(r, h); err != nil {
		return
	}
	n := uint64(h[1] & 0x7f)
	if n >= 126 {
		ext := make([]byte, 2+6*(n-126)) // 126: two bytes of length, 127: eight
		if _, err = io.ReadFull(r, ext); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(append(make([]byte, 8-len(ext)), ext...))
	}
	if n > 8<<20 {
		err = fmt.Errorf("a frame of %d bytes, over 8 MiB", n)
		return
	}
	k := make([]byte, 4*int(h[1]>>7)) // a masked frame carries its key
	p = make([]byte, n)
	if _, err = io.ReadFull(r, k); err == nil {
		_, err = io.ReadFull(r, p)
	}
	for i := range p {
		if len(k) == 4 {
			p[i] ^= k[i%4]
		}
	}
	return h[0]&0x80 != 0, h[0] & 0x0f, p, err
}

// proxyProc is a started proxy: its stdin and stdout as one stream. When stdout ends, the error says why from stderr.
type proxyProc struct {
	cmd *exec.Cmd
	io.WriteCloser
	out    io.Reader
	stderr *bytes.Buffer
	waited sync.Once // Read at the end of stdout and Close (the timer's goroutine) may both wait
}

// startProxy starts exe with args, its stdin and stdout piped.
func startProxy(exe string, args ...string) (io.ReadWriteCloser, error) {
	cmd := exec.Command(exe, args...)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	p := &proxyProc{cmd: cmd, WriteCloser: in, out: out, stderr: new(bytes.Buffer)}
	cmd.Stderr = p.stderr
	return p, cmd.Start()
}

func (p *proxyProc) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF {
		p.wait() // stderr is whole once the proxy has exited
		err = proxyExit(p.stderr.String())
	}
	return n, err
}

func (p *proxyProc) Close() error {
	p.WriteCloser.Close()
	p.cmd.Process.Kill()
	p.wait()
	return nil
}

func (p *proxyProc) wait() { p.waited.Do(func() { p.cmd.Wait() }) }

// proxyExit is why the proxy ended, from what it wrote to stderr. It fails to connect when no daemon runs.
func proxyExit(stderr string) error {
	if strings.Contains(strings.ToLower(stderr), "failed to connect") {
		return errors.New("the Codex daemon is not running (the alarm never starts it)")
	}
	return fmt.Errorf("the Codex proxy exited: %q", clip(strings.TrimSpace(stderr), 200))
}
