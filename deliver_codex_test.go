package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// cxDaemon is a fake Codex daemon behind a fake proxy: it speaks the WebSocket and JSON-RPC the deliverer speaks, and
// records every proxy it was started as and every request it got.
type cxDaemon struct {
	mu      sync.Mutex
	runs    []string          // the arguments of each start
	calls   []string          // method and params of each request and notice
	answers map[string]string // a method's result, as JSON; "!" + message answers with an error
	mute    bool              // the daemon never answers
}

// cxFake installs a fake daemon, and the daemon's executable it is reached through, for one test.
func cxFake(t *testing.T, answers map[string]string) *cxDaemon {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	os.MkdirAll(filepath.Dir(codexDaemonExe()), 0o755)
	os.WriteFile(codexDaemonExe(), nil, 0o755)
	d := &cxDaemon{answers: answers}
	old := codexRun
	codexRun = d.start
	t.Cleanup(func() { codexRun = old })
	return d
}

func (d *cxDaemon) start(exe string, args ...string) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	d.runs = append(d.runs, strings.Join(args, " "))
	d.mu.Unlock()
	if exe != codexDaemonExe() {
		return nil, fmt.Errorf("started %s, not the daemon's own executable", exe)
	}
	near, far := net.Pipe()
	go d.serve(far)
	return near, nil
}

// serve is the far end: the upgrade, then one answer per request, with a ping, a notice and a split frame on the way.
func (d *cxDaemon) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for line := "x"; strings.TrimSpace(line) != ""; {
		var err error
		if line, err = br.ReadString('\n'); err != nil {
			return
		}
	}
	io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	for {
		h, err := br.Peek(2)
		if err != nil {
			return
		}
		if h[1]&0x80 == 0 {
			d.mu.Lock()
			d.calls = append(d.calls, "an unmasked frame")
			d.mu.Unlock()
			return
		}
		fin, op, p, err := wsRead(br)
		if err != nil || !fin {
			return
		}
		if op != 1 { // the pong
			continue
		}
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(p, &m)
		d.mu.Lock()
		d.calls = append(d.calls, m.Method+" "+string(m.Params))
		d.mu.Unlock()
		if m.ID == nil || d.mute {
			continue
		}
		go d.answer(c, *m.ID, m.Method) // while it writes, the loop reads the pong: a net.Pipe holds nothing
	}
}

func (d *cxDaemon) answer(c net.Conn, id int, method string) {
	wsWrite(c, 9, []byte("ping"), false)
	wsWrite(c, 1, []byte(`{"method":"thread/status/changed","params":{}}`), false)
	wsWrite(c, 1, []byte(`{"id":999,"result":{}}`), false) // someone else's answer
	res := d.answers[method]
	if res == "" {
		res = "{}"
	}
	body := fmt.Sprintf(`{"id":%d,"result":%s}`, id, res)
	if msg, ok := strings.CutPrefix(res, "!"); ok {
		body = fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`, id, msg)
	}
	// The answer in two frames: a text frame of 10 bytes that does not end, then its continuation.
	c.Write(append([]byte{0x01, 10}, body[:10]...))
	wsWrite(c, 0, []byte(body[10:]), false)
}

// sent are the requests and notices the daemon got, one method per line, with the params of the turn methods.
func (d *cxDaemon) sent() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if !strings.HasPrefix(c, "turn/") && !strings.HasPrefix(c, "thread/") {
			c, _, _ = strings.Cut(c, " ")
		}
		out = append(out, c)
	}
	return strings.Join(out, "\n")
}

const cxThreads = `{"data":[
	{"id":"t-idle","cwd":"D:\\Work\\acme","status":{"type":"idle"},"source":"cli","updatedAt":100},
	{"id":"t-busy","cwd":"D:\\Work\\acme","status":{"type":"active","activeFlags":[]},"source":"vscode","updatedAt":200},
	{"id":"t-closed","cwd":"D:\\Work\\acme","status":{"type":"notLoaded"},"source":"cli","updatedAt":300},
	{"id":"t-exec","cwd":"D:\\Work\\acme","status":{"type":"idle"},"source":"exec","updatedAt":50},
	{"id":"t-sub","cwd":"D:\\Work\\acme","status":{"type":"idle"},"source":{"subAgent":{"thread_spawn":{}}},"updatedAt":60},
	{"id":"t-idle","cwd":"D:\\Work\\acme","status":{"type":"idle"},"source":"cli","updatedAt":30},
	{"id":"t-busy","cwd":"D:\\Work\\acme","status":{"type":"active","activeFlags":[]},"source":"vscode","updatedAt":250}]}`

// SPEC A6, Codex: the live threads are the ones the daemon holds loaded, each with its folder, whether a program
// started it and when it was used, once each (the daemon lists some threads several times, live 2026-11-17: the
// entry used last counts). A message to an idle thread starts a turn; to a busy one it steers the running
// turn, named by its id. A thread the daemon does not hold, or a running turn it does not list, gets nothing.
func TestCodexDelivererStartsIdleSteersBusy(t *testing.T) {
	d := cxFake(t, map[string]string{"thread/list": cxThreads,
		"thread/turns/list": `{"data":[{"id":"turn-9","status":"inProgress"}]}`})
	live, err := codexDeliverer.live()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	byID := map[string]liveTarget{}
	for _, s := range live {
		got = append(got, fmt.Sprintf("%s %s %s %v %d", s.via, s.id, s.cwd, s.program, s.used.UnixNano()/1e9))
		if s.used.Nanosecond() != 0 {
			t.Errorf("%s used at %s, not a whole second", s.id, s.used)
		}
		byID[s.id] = s
	}
	want := `codex t-idle D:\Work\acme false 100|codex t-busy D:\Work\acme false 250|codex t-exec D:\Work\acme true 50|codex t-sub D:\Work\acme true 60`
	if strings.Join(got, "|") != want {
		t.Fatalf("live threads:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	d.calls, d.answers["thread/read"] = nil, `{"thread":{"id":"t-idle","status":{"type":"idle"}}}`
	if err := byID["t-idle"].send("wake up"); err != nil {
		t.Fatal(err)
	}
	if s := d.sent(); s != "initialize\ninitialized\n"+cxRead("t-idle")+"\nturn/start "+`{"input":[{"text":"wake up","type":"text"}],"threadId":"t-idle"}` {
		t.Errorf("an idle thread got:\n%s", s)
	}
	d.calls, d.answers["thread/read"] = nil, `{"thread":{"id":"t-busy","status":{"type":"active"}}}`
	if err := byID["t-busy"].send("wake up"); err != nil {
		t.Fatal(err)
	}
	if s := d.sent(); s != "initialize\ninitialized\n"+cxRead("t-busy")+"\nthread/turns/list "+`{"limit":1,"threadId":"t-busy"}`+"\nturn/steer "+`{"expectedTurnId":"turn-9","input":[{"text":"wake up","type":"text"}],"threadId":"t-busy"}` {
		t.Errorf("a busy thread got:\n%s", s)
	}
	for read, wantErr := range map[string]string{
		`{"thread":{"status":{"type":"notLoaded"}}}`: "notLoaded, not idle or running",
		`{"thread":{}}`:     "unknown, not idle or running",
		`!thread not found`: "thread/read: thread not found (-32600)",
	} {
		d.calls, d.answers["thread/read"] = nil, read
		if err := byID["t-idle"].send("x"); err == nil || !strings.Contains(err.Error(), wantErr) || strings.Contains(d.sent(), "turn/") {
			t.Errorf("read %s: %v, sent %s", read, err, d.sent())
		}
	}
	d.answers["thread/read"] = `{"thread":{"status":{"type":"active"}}}`
	for _, turns := range []string{`{"data":[{"id":"turn-8","status":{"type":"completed"}}]}`, `{"data":[]}`, `!busy`} {
		d.calls, d.answers["thread/turns/list"] = nil, turns
		if err := byID["t-busy"].send("x"); err == nil || strings.Contains(d.sent(), "turn/steer") {
			t.Errorf("turns %s: %v, sent %s", turns, err, d.sent())
		}
	}
	d.answers["thread/list"] = "!overloaded"
	if _, err := codexDeliverer.live(); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("a refused list: %v", err)
	}
}

// cxRead is the thread/read request for one thread, as the fake daemon records it.
func cxRead(id string) string { return `thread/read {"includeTurns":false,"threadId":"` + id + `"}` }

// SPEC A6: the deliverer never starts the daemon. It runs only the daemon's own executable, only as `app-server
// proxy`; with no daemon installed it lists nothing and starts nothing; a daemon that is not running, or does not
// answer, is a failed delivery.
func TestCodexDelivererNeverStartsDaemon(t *testing.T) {
	d := cxFake(t, map[string]string{})
	os.Remove(codexDaemonExe())
	if live, err := codexDeliverer.live(); live != nil || err != nil || len(d.runs) != 0 {
		t.Fatalf("no daemon installed: %v, %v, started %v", live, err, d.runs)
	}
	os.WriteFile(codexDaemonExe(), nil, 0o755)
	old := codexRun
	codexRun = func(exe string, args ...string) (io.ReadWriteCloser, error) {
		d.start(exe, args...)
		return failingProxy{proxyExit("Error: failed to connect to the app-server daemon")}, nil
	}
	if _, err := codexDeliverer.live(); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("no daemon running: %v", err)
	}
	if err := codexSend(codexDaemonExe(), "t-1", "x"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("a send with no daemon running: %v", err)
	}
	codexRun = old
	d.mute = true
	oldWait := codexWait
	codexWait = 100 * time.Millisecond
	t.Cleanup(func() { codexWait = oldWait })
	if _, err := codexDeliverer.live(); err == nil {
		t.Error("a daemon that never answers")
	}
	for _, r := range d.runs {
		if r != "app-server proxy" {
			t.Errorf("started the daemon's executable as %q", r)
		}
	}
	if len(d.runs) != 3 {
		t.Errorf("started %d proxies, want 3", len(d.runs))
	}
	if err := proxyExit("thread panicked"); err.Error() != `the Codex proxy exited: "thread panicked"` {
		t.Errorf("another proxy exit: %v", err)
	}
}

// failingProxy is a proxy whose stream ends with err at once.
type failingProxy struct{ err error }

func (f failingProxy) Read([]byte) (int, error)    { return 0, f.err }
func (f failingProxy) Write(b []byte) (int, error) { return len(b), nil }
func (f failingProxy) Close() error                { return nil }

// startProxy runs a real process with its stdio piped: the test binary, which exits at once with a subcommand. Its end
// is an error that says the proxy exited; a program that does not exist does not start.
func TestStartProxy(t *testing.T) {
	p, err := startProxy(os.Args[0], "app-server", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	if _, err := io.ReadAll(p); err == nil || !strings.Contains(err.Error(), "proxy exited") {
		t.Errorf("the end of the proxy: %v", err)
	}
	if _, err := startProxy(filepath.Join(t.TempDir(), "none.exe")); err == nil {
		t.Error("a missing program started")
	}
}

// The frames: lengths of every size class round-trip, a refused upgrade is an error, a frame over 8 MiB is refused.
func TestWebSocketFrames(t *testing.T) {
	for _, n := range []int{0, 125, 126, 65535, 65536} {
		var b strings.Builder
		p := []byte(strings.Repeat("a", n))
		wsWrite(&b, 1, p, true)
		fin, op, got, err := wsRead(bufio.NewReader(strings.NewReader(b.String())))
		if err != nil || !fin || op != 1 || string(got) != string(p) {
			t.Errorf("%d bytes: %v %v %d %d", n, err, fin, op, len(got))
		}
		hdr := b.String()[1]
		if hdr&0x80 == 0 {
			t.Errorf("%d bytes: a client frame is not masked", n)
		}
		if want := map[int]byte{0: 0, 125: 125, 126: 126, 65535: 126, 65536: 127}[n]; hdr&0x7f != want {
			t.Errorf("%d bytes: length byte %d, want %d", n, hdr&0x7f, want)
		}
	}
	if _, _, _, err := wsRead(bufio.NewReader(strings.NewReader("\x81\x7f\x00\x00\x00\x00\x00\x80\x00\x01"))); err == nil {
		t.Error("a frame one byte over 8 MiB was read")
	}
	var big strings.Builder
	wsWrite(&big, 1, make([]byte, 8<<20), false)
	if _, _, p, err := wsRead(bufio.NewReader(strings.NewReader(big.String()))); err != nil || len(p) != 8<<20 {
		t.Errorf("a frame of 8 MiB: %d bytes, %v", len(p), err)
	}
	if want := map[string]string{"windows": "codex.exe"}[runtime.GOOS]; filepath.Base(codexDaemonExe()) != cmp.Or(want, "codex") {
		t.Errorf("the daemon's executable is %s", codexDaemonExe())
	}
	for _, short := range []string{"\x81", "\x81\x7e\x00", "\x81\x05ab"} {
		if _, _, _, err := wsRead(bufio.NewReader(strings.NewReader(short))); err == nil {
			t.Errorf("a cut frame %q was read", short)
		}
	}
	c := &codexConn{rw: rwOf("HTTP/1.1 403 Forbidden\r\n\r\n"), timer: time.NewTimer(time.Hour)}
	c.br = bufio.NewReader(c.rw)
	if err := c.handshake(); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a refused upgrade: %v", err)
	}
	for _, cut := range []string{"", "HTTP/1.1 101 Switching Protocols\r\nUpgrade: web"} {
		c = &codexConn{rw: rwOf(cut), timer: time.NewTimer(time.Hour)}
		c.br = bufio.NewReader(c.rw)
		if err := c.handshake(); err == nil {
			t.Errorf("a cut upgrade %q", cut)
		}
	}
	closing := &codexConn{rw: rwOf("\x88\x00"), timer: time.NewTimer(time.Hour)}
	closing.br = bufio.NewReader(closing.rw)
	if _, err := closing.next(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("a close frame: %v", err)
	}
	pong := &codexConn{rw: rwOf("\x8a\x00\x81\x02{}"), timer: time.NewTimer(time.Hour)}
	pong.br = bufio.NewReader(pong.rw)
	if m, err := pong.next(); err != nil || string(m) != "{}" {
		t.Errorf("a pong, then a message: %q %v", m, err)
	}
}

// rwOf is a stream that reads s and takes any write.
func rwOf(s string) io.ReadWriteCloser {
	return struct {
		io.Reader
		io.Writer
		io.Closer
	}{strings.NewReader(s), io.Discard, io.NopCloser(nil)}
}

// A request the proxy cannot take is an error, before any answer is read.
func TestCodexWriteFails(t *testing.T) {
	c := &codexConn{rw: struct {
		io.Reader
		io.Writer
		io.Closer
	}{strings.NewReader("HTTP/1.1 101 Switching Protocols\r\n\r\n"), failWriter{}, io.NopCloser(nil)}, timer: time.NewTimer(time.Hour)}
	c.br = bufio.NewReader(c.rw)
	if err := c.handshake(); err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Errorf("a proxy that takes no request: %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
