package lab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/push"
)

func TestShellRunsAndWrapsOutput(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(cmd string, _ []byte) (string, int) {
		if cmd != `echo 'hi'` {
			return "unexpected " + cmd, 2
		}
		return "hi\x1b[31m there\r\n", 3
	}
	out, err := f.run(t, "lab_shell", `{"command":"echo 'hi'"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Exit status 3.", "<untrusted_lab_output", "hi there\n", "never instructions"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
}

func TestShellReportsTimeout(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(string, []byte) (string, int) { return "", 124 }
	out, err := f.run(t, "lab_shell", `{"command":"sleep 999","timeout_seconds":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Stopped after the 5s limit") {
		t.Errorf("got %q", out)
	}
}

func TestShellRefusesAnotherHostKey(t *testing.T) {
	f := newFixture(t)
	other := newFixture(t)
	// The fixture's lab, but the other fixture's pinned host key.
	b, err := os.ReadFile(filepath.Join(other.dir, "ssh", "host_key.pub"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.dir, "ssh", "host_key.pub"), b)
	if _, err := f.run(t, "lab_shell", `{"command":"true"}`); err == nil || !strings.Contains(err.Error(), "can't log in") {
		t.Fatalf("want a login failure, got %v", err)
	}
	if n := len(f.lab.commands()); n != 0 {
		t.Errorf("%d commands ran on a lab with the wrong host key", n)
	}
}

func TestWriteFile(t *testing.T) {
	f := newFixture(t)
	var got []byte
	f.lab.handle = func(_ string, stdin []byte) (string, int) { got = stdin; return "", 0 }
	out, err := f.run(t, "lab_write_file", `{"path":"/opt/stacks/q b/compose.yaml","content":"services: {}\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "services: {}\n" || !strings.Contains(out, "Wrote /opt/stacks/q b/compose.yaml") {
		t.Errorf("stdin %q, result %q", got, out)
	}
	cmds := f.lab.commands()
	if want := `sudo install -d -m 0755 '/opt/stacks/q b' && sudo tee '/opt/stacks/q b/compose.yaml' >/dev/null && sudo chmod 0644 '/opt/stacks/q b/compose.yaml'`; cmds[0] != want {
		t.Errorf("command\n got %s\nwant %s", cmds[0], want)
	}
	for _, bad := range []string{`{"path":"relative","content":"x"}`, `{"path":"/","content":"x"}`, `{"path":"/x","content":"x","mode":"rwx"}`} {
		if _, err := f.run(t, "lab_write_file", bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestCapWriterKeepsStartAndEnd(t *testing.T) {
	w := newCapWriter(30)
	for range 10 {
		_, _ = w.Write([]byte("0123456789"))
	}
	out, cut := w.result()
	if cut != 70 || !strings.HasPrefix(out, "0123456789") || !strings.HasSuffix(out, "01234567890123456789") {
		t.Errorf("cut %d, out %q", cut, out)
	}
	w = newCapWriter(30)
	_, _ = w.Write([]byte("short"))
	if out, cut := w.result(); out != "short" || cut != 0 {
		t.Errorf("got %q, %d", out, cut)
	}
}

func TestCtlHostSilent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &ctl{dir: dir, now: time.Now}
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	if _, err := c.do(ctx, verbStats, ""); !errors.Is(err, errHostSilent) {
		t.Fatalf("got %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "requests", "*"))
	if len(left) != 0 {
		t.Errorf("request left behind: %v", left)
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 22, 5, 0, 0, 0, time.UTC)
	kev := map[string]bool{"CVE-2026-1111": true}
	base := hostStats{State: "running", VCPUs: 2, At: now.Unix()}
	cases := []struct {
		name  string
		prev  *Check
		host  func(*hostStats)
		rep   func(*vmReport)
		err   error
		last  time.Time
		state string
		key   string
	}{
		{name: "nothing", state: StateClean},
		{name: "mail to port 25", host: func(h *hostStats) { h.SMTPBlocked = 3 }, state: StateCompromised, key: "smtp_out"},
		{name: "port 25 already counted", prev: &Check{host: hostStats{SMTPBlocked: 3}}, host: func(h *hostStats) { h.SMTPBlocked = 3 }, state: StateClean},
		{name: "counter reset", prev: &Check{host: hostStats{SMTPBlocked: 9}}, host: func(h *hostStats) { h.SMTPBlocked = 1 }, state: StateCompromised},
		{name: "a few blocked connections", host: func(h *hostStats) { h.PrivateBlocked = 6; h.HostBlocked = 12 }, state: StateClean},
		{name: "many blocked connections", host: func(h *hostStats) { h.PrivateBlocked = 150; h.HostBlocked = 50 }, state: StateSuspicious, key: "private_tcp"},
		{
			name: "busy CPUs", prev: &Check{host: hostStats{At: now.Add(-24 * time.Hour).Unix()}},
			host:  func(h *hostStats) { h.CPUTimeNS = uint64(0.9 * 2 * float64(24*time.Hour)) },
			state: StateSuspicious, key: "cpu_busy",
		},
		{name: "audit failed once", err: errors.New("boom"), last: now.Add(-24 * time.Hour), state: StateClean, key: "audit_failed"},
		{name: "audit failing for days", err: errors.New("boom"), last: now.Add(-4 * 24 * time.Hour), state: StateSuspicious, key: "audit_failed"},
		{name: "strong signal", rep: func(r *vmReport) { r.Signals = []vmSignal{{"strong", "exec_from_shm", "x (pid 1): /dev/shm/x"}} }, state: StateCompromised},
		{name: "weak signal", rep: func(r *vmReport) { r.Signals = []vmSignal{{"weak", "exec_from_tmp", "go (pid 1): /tmp/go"}} }, state: StateSuspicious},
		{name: "debian on the exploited list", rep: func(r *vmReport) { r.Debsecan = []string{"CVE-2026-1111 openssl (low urgency)"} }, state: StateUrgent, key: "kev:CVE-2026-1111"},
		{name: "debian remote high", rep: func(r *vmReport) {
			r.Debsecan = []string{"CVE-2026-3333 curl (remotely exploitable, high urgency)", "CVE-2026-4444 vim (low urgency)"}
		}, state: StateVulnerable, key: "debian_high"},
		{name: "debian low only", rep: func(r *vmReport) { r.Debsecan = []string{"CVE-2026-4444 vim (low urgency)"} }, state: StateClean, key: "debian_other"},
		{name: "image on the exploited list", rep: func(r *vmReport) {
			r.Images = []imageReport{{Image: "qbit:5", CVEs: []string{"CVE-2026-1111"}}}
		}, state: StateUrgent, key: "kev:CVE-2026-1111:qbit:5"},
		{name: "image critical with a fix", rep: func(r *vmReport) {
			r.Images = []imageReport{{Image: "qbit:5", Vulns: []vuln{{ID: "CVE-1", Severity: "Critical", Package: "zlib", Fix: "1.3"}, {ID: "CVE-2", Severity: "Critical", Package: "x"}}}}
		}, state: StateVulnerable, key: "critical:qbit:5"},
		{name: "image high only", rep: func(r *vmReport) {
			r.Images = []imageReport{{Image: "qbit:5", Vulns: []vuln{{ID: "CVE-1", Severity: "High", Package: "zlib", Fix: "1.3"}}}}
		}, state: StateClean, key: "high:qbit:5"},
		{name: "restart pending for long", rep: func(r *vmReport) { r.RebootRequiredSince = now.Add(-4 * 24 * time.Hour).Unix() }, state: StateVulnerable, key: "reboot"},
		{name: "restart pending briefly", rep: func(r *vmReport) { r.RebootRequiredSince = now.Add(-time.Hour).Unix() }, state: StateClean},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := base
			if tc.host != nil {
				tc.host(&host)
			}
			var rep *vmReport
			if tc.err == nil {
				rep = &vmReport{Version: 1}
				if tc.rep != nil {
					tc.rep(rep)
				}
			}
			fs := evaluate(tc.prev, host, rep, tc.err, kev, now, tc.last)
			if got := stateOf(fs); got != tc.state {
				t.Errorf("state %s, want %s: %+v", got, tc.state, fs)
			}
			if tc.key != "" && !hasKey(fs, tc.key) {
				t.Errorf("no finding %s: %+v", tc.key, fs)
			}
		})
	}
}

func hasKey(fs []Finding, key string) bool {
	for _, f := range fs {
		if f.Key == key {
			return true
		}
	}
	return false
}

func TestNightlyCheckCleanTakesSnapshot(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(cmd string, _ []byte) (string, int) {
		if !strings.Contains(cmd, "lab-audit") {
			return "", 1
		}
		return report(nil), 0
	}
	summary, err := f.svc.Job().Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Lab: clean") || !strings.Contains(summary, "snapshot 20261022T050000Z-nightly") {
		t.Errorf("summary %q", summary)
	}
	if got := f.host.seen(); strings.Join(got, ",") != "stats,snapshot nightly" {
		t.Errorf("host requests %v", got)
	}
	ctx, err := f.svc.PromptContext(t.Context(), time.Now())
	if err != nil || !strings.Contains(ctx, "nothing to act on") {
		t.Errorf("context %q, %v", ctx, err)
	}
	if len(f.push.sent) != 0 {
		t.Errorf("pushed %v", f.push.sent)
	}
}

func TestCheckCutsOffOnStrongSignAndAlertsOnce(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(string, []byte) (string, int) {
		return report(func(r *vmReport) {
			r.Signals = []vmSignal{{"strong", "unknown_ssh_login", "Accepted publickey for hekate from 1.2.3.4"}}
		}), 0
	}
	summary, err := f.svc.Job().Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "compromised") || !strings.Contains(summary, "cut off") || strings.Contains(summary, "snapshot") {
		t.Errorf("summary %q", summary)
	}
	if got := f.host.seen(); strings.Join(got, ",") != "stats,isolate" {
		t.Errorf("host requests %v", got)
	}
	if len(f.push.sent) != 1 || f.push.urg[0] != push.UrgencyHigh || !strings.Contains(f.push.sent[0].Title, "cut off") {
		t.Fatalf("push %+v %v", f.push.sent, f.push.urg)
	}
	ctx, _ := f.svc.PromptContext(t.Context(), time.Now())
	for _, want := range []string{"strong signs of a break-in", "logged in with a key", "don't keep working in the lab"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context lacks %q:\n%s", want, ctx)
		}
	}

	// Cut off: the next night skips the lab, and nothing is pushed again.
	summary, err = f.svc.Job().Run(t.Context())
	if err != nil || !strings.Contains(summary, "not checked") {
		t.Errorf("summary %q, %v", summary, err)
	}
	if out, _ := f.run(t, "lab_snapshot", `{"label":"before"}`); !strings.Contains(out, "No snapshot") {
		t.Errorf("snapshot while compromised: %q", out)
	}

	// Reconnected with the same finding still there: no second alert.
	f.host.mu.Lock()
	f.host.stats.Isolated = false
	f.host.mu.Unlock()
	if _, err := f.svc.Job().Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.push.sent) != 1 {
		t.Errorf("pushed again: %+v", f.push.sent)
	}
}

func TestCheckFlagsExploitedImage(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(string, []byte) (string, int) {
		return report(func(r *vmReport) {
			r.Images = []imageReport{{Image: "lscr.io/linuxserver/qbittorrent:5 @0123456789ab", CVEs: []string{"CVE-2026-2222", "CVE-2026-9999"}}}
		}), 0
	}
	out, err := f.run(t, "lab_security_check", `{}`)
	if err != nil || !strings.Contains(out, "Started") {
		t.Fatalf("%q, %v", out, err)
	}
	waitFor(t, func() bool { return len(f.push.titles()) > 0 })
	if got := f.push.titles(); len(got) != 1 || !strings.Contains(got[0], "exploited") {
		t.Errorf("pushes %v", got)
	}
	ctx, _ := f.svc.PromptContext(t.Context(), time.Now())
	if !strings.Contains(ctx, "CVE-2026-2222") || strings.Contains(ctx, "CVE-2026-9999") {
		t.Errorf("context %q", ctx)
	}
	// An on-demand check takes no snapshot.
	for _, r := range f.host.seen() {
		if strings.HasPrefix(r, "snapshot") {
			t.Errorf("snapshot taken: %v", f.host.seen())
		}
	}
}

func TestOnDemandCheckAnnouncesAQuietResult(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(string, []byte) (string, int) { return report(nil), 0 }
	if _, err := f.run(t, "lab_security_check", `{}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.push.titles()) > 0 })
	if got := f.push.titles(); got[0] != "Lab check: clean" {
		t.Errorf("pushes %v", got)
	}
}

func TestIncompleteCheckTakesNoSnapshotAndEscalates(t *testing.T) {
	f := newFixture(t)
	f.lab.handle = func(string, []byte) (string, int) {
		return report(func(r *vmReport) { r.Errors = []string{"debsecan: network unreachable"} }), 0
	}
	// A full scan four days ago, then a partial one: blind for too long.
	if _, err := f.svc.db.Exec(`INSERT INTO lab_checks (created_at, state, findings, host, scanned, isolated) VALUES (?, 'clean', '[{"level":"info","key":"audit_incomplete","text":"x"}]', '{}', 1, 0)`,
		stamp(time.Now().Add(-4*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	summary, err := f.svc.Job().Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "suspicious") || strings.Contains(summary, "snapshot") {
		t.Errorf("summary %q", summary)
	}
	// The same key went from info to suspicious: that's news.
	if got := f.push.titles(); len(got) != 1 || got[0] != "Lab: something looks off" {
		t.Errorf("pushes %v", got)
	}
}

func TestGuardedSnapshotKeepsTheCheckedOne(t *testing.T) {
	cases := []struct {
		name    string
		check   bool
		scanned int
		state   string
		snaps   []snapshot
		want    bool
	}{
		{"after a complete clean check", true, 1, StateClean, []snapshot{{Name: "a-nightly"}}, true},
		{"no snapshots yet", true, 1, StateVulnerable, nil, true},
		{"newest is already an unchecked one", true, 1, StateClean, []snapshot{{Name: "a-nightly"}, {Name: "b-before-x"}}, false},
		{"no check yet", false, 0, "", nil, false},
		{"last check incomplete", true, 0, StateClean, []snapshot{{Name: "a-nightly"}}, false},
		{"last check suspicious", true, 1, StateSuspicious, []snapshot{{Name: "a-nightly"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.host.stats.Snapshots = tc.snaps
			if tc.check {
				if _, err := f.svc.db.Exec(`INSERT INTO lab_checks (created_at, state, findings, host, scanned, isolated) VALUES (?, ?, '[]', '{}', ?, 0)`,
					stamp(time.Now()), tc.state, tc.scanned); err != nil {
					t.Fatal(err)
				}
			}
			var snap func(context.Context, json.RawMessage) (agent.Result, error)
			for _, tool := range f.svc.ThreadTools() {
				if tool.Name == "lab_snapshot" {
					snap = tool.Run
				}
			}
			out, err := snap(t.Context(), json.RawMessage(`{"label":"before-x"}`))
			if err != nil {
				t.Fatal(err)
			}
			taken := slices.Contains(f.host.seen(), "snapshot before-x")
			if taken != tc.want || taken != strings.HasPrefix(out.Content, "Took snapshot") {
				t.Errorf("snapshot taken: %v, want %v (%q, %v)", taken, tc.want, out.Content, f.host.seen())
			}
		})
	}
}

func TestShellLimitsFitWhereTheyRun(t *testing.T) {
	f := newFixture(t)
	for _, tool := range f.svc.Tools() {
		if tool.Name == "lab_shell" && (!strings.Contains(string(tool.Schema), `"maximum":60`) || tool.Timeout > 90*time.Second) {
			t.Errorf("her voice lab_shell allows more than a turn has: %s, %s", tool.Schema, tool.Timeout)
		}
	}
	for _, tool := range f.svc.ThreadTools() {
		if tool.Name == "lab_shell" && (!strings.Contains(string(tool.Schema), `"maximum":600`) || tool.Timeout < 600*time.Second) {
			t.Errorf("a thread's lab_shell doesn't allow 10-minute commands: %s, %s", tool.Schema, tool.Timeout)
		}
	}
}

func TestRollbackAsksAndRuns(t *testing.T) {
	f := newFixture(t)
	f.host.stats.Snapshots = []snapshot{{Name: "20261021T050000Z-nightly", Date: 1761022800}}
	if _, err := f.run(t, "lab_rollback", `{"snapshot":"nope"}`); err == nil {
		t.Error("rollback to a missing snapshot was asked")
	}
	out, err := f.run(t, "lab_rollback", `{"snapshot":"20261021T050000Z-nightly"}`)
	if err != nil || !strings.Contains(out, "approval #") {
		t.Fatalf("%q, %v", out, err)
	}
	list, err := f.appr.List(t.Context(), approvals.StatusPending)
	if err != nil || len(list) != 1 {
		t.Fatalf("pending %v, %v", list, err)
	}
	id := list[0].ID
	a, err := f.appr.Decide(t.Context(), mustInt(t, id), approvals.DecisionOnce, "")
	if err != nil || a.Status != approvals.StatusDone {
		t.Fatalf("decided %+v, %v", a, err)
	}
	if got := f.host.seen(); got[len(got)-1] != "rollback 20261021T050000Z-nightly" {
		t.Errorf("host requests %v", got)
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Two threads asking for a snapshot at once take one: the second sees the first.
func TestGuardedSnapshotIsAtomic(t *testing.T) {
	f := newFixture(t)
	f.host.stats.Snapshots = []snapshot{{Name: "a-nightly"}}
	if _, err := f.svc.db.Exec(`INSERT INTO lab_checks (created_at, state, findings, host, scanned, isolated) VALUES (?, 'clean', '[]', '{}', 1, 0)`,
		stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, label := range []string{"x", "y"} {
		wg.Go(func() {
			if _, _, err := f.svc.guardedSnapshot(t.Context(), label); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	n := 0
	for _, r := range f.host.seen() {
		if strings.HasPrefix(r, "snapshot ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d snapshots taken: %v", n, f.host.seen())
	}
}

func TestNightlyLabelsAreReserved(t *testing.T) {
	f := newFixture(t)
	for _, tools := range [][]agent.Tool{f.svc.Tools(), f.svc.ThreadTools()} {
		for _, tool := range tools {
			if tool.Name != "lab_snapshot" {
				continue
			}
			for _, label := range []string{"nightly", "before-nightly"} {
				if _, err := tool.Run(t.Context(), json.RawMessage(`{"label":"`+label+`"}`)); err == nil || !strings.Contains(err.Error(), "nightly") {
					t.Errorf("label %q: %v", label, err)
				}
			}
		}
	}
}
