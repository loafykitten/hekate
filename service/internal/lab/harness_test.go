package lab

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// fakeLab is an SSH server standing in for the lab VM. handle gets the command she asked for
// (unwrapped from the timeout/bash wrapper) and what was sent on stdin.
type fakeLab struct {
	mu     sync.Mutex
	cmds   []string
	handle func(cmd string, stdin []byte) (out string, exit int)
}

func (f *fakeLab) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// fakeHost answers hekate-labctl requests from the requests directory, as the host would.
type fakeHost struct {
	mu       sync.Mutex
	requests []string // "verb arg"
	stats    hostStats
	fail     map[string]string // verb -> error
}

func (h *fakeHost) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests...)
}

func (h *fakeHost) answer(verb, arg string) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, strings.TrimSpace(verb+" "+arg))
	if msg, ok := h.fail[verb]; ok {
		return nil, errors.New(msg)
	}
	switch verb {
	case verbStats:
		return h.stats, nil
	case verbSnapshot:
		name := "20261022T050000Z-" + arg
		h.stats.Snapshots = append(h.stats.Snapshots, snapshot{Name: name, Date: 1})
		return map[string]string{"name": name}, nil
	case verbIsolate:
		h.stats.Isolated, h.stats.IsolatedSince = true, "2026-10-22T05:00:00Z"
		return map[string]bool{"isolated": true}, nil
	case verbReconnect:
		h.stats.Isolated, h.stats.IsolatedSince = false, ""
		return map[string]bool{"isolated": false}, nil
	case verbRollback:
		return map[string]string{"name": arg}, nil
	}
	return nil, errors.New("unknown request")
}

type recEvents struct {
	mu    sync.Mutex
	notes []proto.Notify
}

func (r *recEvents) Invalidate(...string) {}

func (r *recEvents) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

type recPush struct {
	mu   sync.Mutex
	sent []push.Payload
	urg  []push.Urgency
}

func (p *recPush) Send(_ context.Context, pl push.Payload, u push.Urgency) (push.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, pl)
	p.urg = append(p.urg, u)
	return push.Result{}, nil
}

// fakeLLM plays the worker's model: each round takes the next scripted step; with none left it
// answers "All done." without tools.
type fakeLLM struct {
	mu    sync.Mutex
	steps []func(ctx context.Context, req llm.Request) (string, []llm.ToolCall, error)
	reqs  []llm.Request
}

func (f *fakeLLM) Round(ctx context.Context, req llm.Request) (string, []llm.ToolCall, *llm.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	var step func(context.Context, llm.Request) (string, []llm.ToolCall, error)
	if len(f.steps) > 0 {
		step, f.steps = f.steps[0], f.steps[1:]
	}
	f.mu.Unlock()
	if step == nil {
		return "All done.", nil, nil, nil
	}
	text, calls, err := step(ctx, req)
	return text, calls, nil, err
}

func (f *fakeLLM) requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.reqs...)
}

// call is a scripted round that asks for one tool call.
func call(name, args string) func(context.Context, llm.Request) (string, []llm.ToolCall, error) {
	return func(context.Context, llm.Request) (string, []llm.ToolCall, error) {
		return "", []llm.ToolCall{{ID: "c-" + name, Name: name, Arguments: args}}, nil
	}
}

type fixture struct {
	svc    *Service
	llm    *fakeLLM
	lab    *fakeLab
	host   *fakeHost
	events *recEvents
	push   *recPush
	appr   *approvals.Service
	dir    string
}

const testKEV = `{"vulnerabilities":[{"cveID":"CVE-2026-1111"},{"cveID":"CVE-2026-2222"}]}`

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	appr, err := approvals.New(approvals.Options{Store: db})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	for _, d := range []string{"ssh", "requests", "results"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{
		lab:    &fakeLab{handle: func(string, []byte) (string, int) { return "", 0 }},
		host:   &fakeHost{stats: hostStats{State: "running", VCPUs: 4, At: 1000}, fail: map[string]string{}},
		events: &recEvents{}, push: &recPush{}, appr: appr, dir: dir, llm: &fakeLLM{},
	}
	addr := f.serveSSH(t, dir)
	f.serveHost(t, dir)
	kev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, testKEV) }))
	t.Cleanup(kev.Close)

	f.svc, err = New(Options{Store: db, Approvals: appr, Host: addr, Dir: dir, CacheDir: t.TempDir(), Location: time.UTC,
		Events: f.events, Push: f.push, LLM: f.llm})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.kevURL = kev.URL
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.svc.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	waitFor(t, func() bool { f.svc.bgMu.Lock(); defer f.svc.bgMu.Unlock(); return f.svc.bgCtx != nil })
	return f
}

// serveSSH starts the fake lab and writes her key and its host key where the service reads them.
func (f *fixture) serveSSH(t *testing.T, dir string) string {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "ssh", "id_ed25519"), pem.EncodeToMemory(block))
	writeFile(t, filepath.Join(dir, "ssh", "host_key.pub"), ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	sshClientPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != User || string(key.Marshal()) != string(sshClientPub.Marshal()) {
			return nil, errors.New("not allowed")
		}
		return nil, nil
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serveConn(conn, cfg)
		}
	}()
	return ln.Addr().String()
}

func (f *fixture) serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer func() { _ = ch.Close() }()
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var p struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					_ = req.Reply(false, nil)
					return
				}
				_ = req.Reply(true, nil)
				stdin, _ := io.ReadAll(ch)
				cmd := unwrap(p.Command)
				f.lab.mu.Lock()
				f.lab.cmds = append(f.lab.cmds, cmd)
				handle := f.lab.handle
				f.lab.mu.Unlock()
				out, exit := handle(cmd, stdin)
				_, _ = io.WriteString(ch, out)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
				return
			}
		}()
	}
}

// unwrap takes the command back out of shell()'s timeout/bash wrapper.
func unwrap(remote string) string {
	i := strings.Index(remote, "bash -c '")
	if i < 0 {
		return remote
	}
	q := strings.TrimSuffix(remote[i+len("bash -c '"):], "'")
	return strings.ReplaceAll(q, `'\''`, "'")
}

// serveHost answers request files until the test ends.
func (f *fixture) serveHost(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			paths, _ := filepath.Glob(filepath.Join(dir, "requests", "*.json"))
			for _, p := range paths {
				raw, err := os.ReadFile(p)
				_ = os.Remove(p)
				if err != nil {
					continue
				}
				var req struct{ Verb, Arg string }
				_ = json.Unmarshal(raw, &req)
				data, err := f.host.answer(req.Verb, req.Arg)
				ans := map[string]any{"ok": err == nil}
				if err != nil {
					ans["error"] = err.Error()
				} else {
					ans["data"] = data
				}
				b, _ := json.Marshal(ans)
				id := strings.TrimSuffix(filepath.Base(p), ".json")
				_ = os.WriteFile(filepath.Join(dir, "results", id+".json"), b, 0o600)
			}
		}
	}()
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// run calls one of her tools by name.
func (f *fixture) run(t *testing.T, name, args string) (string, error) {
	t.Helper()
	for _, tool := range f.svc.Tools() {
		if tool.Name == name {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			res, err := tool.Run(ctx, json.RawMessage(args))
			return res.Content, err
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// report builds a lab-audit report.
func report(mod func(*vmReport)) string {
	r := vmReport{Version: 1}
	if mod != nil {
		mod(&r)
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// waitFor polls cond for up to ten seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *recPush) titles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.sent))
	for i, pl := range p.sent {
		out[i] = pl.Title
	}
	return out
}

// task reads a lab task's row.
func (f *fixture) task(t *testing.T, id int64) Task {
	t.Helper()
	tk, err := scanTask(f.svc.db.QueryRowContext(t.Context(), `SELECT `+taskColumns+` FROM lab_tasks WHERE id = ?`, id))
	if err != nil {
		t.Fatal(err)
	}
	return tk
}
