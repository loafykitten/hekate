package lab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
)

// States of the lab after a security check, from best to worst.
const (
	StateClean       = "clean"
	StateVulnerable  = "vulnerable"  // fixes are waiting; nothing in it is known to be exploited
	StateUrgent      = "urgent"      // something in it is on CISA's known-exploited list
	StateSuspicious  = "suspicious"  // a weak sign of a break-in, or the check can't see
	StateCompromised = "compromised" // a strong sign of a break-in: the lab is cut off
	levelInfo        = "info"        // a finding that changes nothing
)

var rank = map[string]int{levelInfo: 0, StateClean: 0, StateVulnerable: 1, StateUrgent: 2, StateSuspicious: 3, StateCompromised: 4}

// JobName is the nightly security check's routine.
const JobName = "lab_check"

// Limits and thresholds. Tuned not to cry wolf: a weak sign is reported, only a strong one acts.
const (
	checkAt      = 5 // 05:00, while nobody's using the lab
	checkCatchUp = 12 * time.Hour
	checkTimeout = 45 * time.Minute
	auditTimeout = 40 * time.Minute
	auditMaxOut  = 8 << 20
	blindAfter   = 3 * 24 * time.Hour // no successful check for this long is itself suspicious
	rebootGrace  = 3 * 24 * time.Hour
	// Blocked packets opening connections into private networks before it's odd. The firewall
	// counts every retried SYN, so one blocked connection that times out counts six or seven
	// times; 200 is a few dozen attempts, where a scan sends thousands.
	blockedTCPAlarm = 200
	busyCPU         = 0.85 // average share of all its CPUs since the last check
	busyMinSpan     = 6 * time.Hour
	keepChecks      = 90 * 24 * time.Hour
	maxFindingText  = 300
	maxVMText       = 120
)

// Finding is one thing a check noticed.
type Finding struct {
	Level string `json:"level"` // the state it alone puts the lab in, or info
	Key   string `json:"key"`   // the same from check to check, to tell new findings from known ones
	Text  string `json:"text"`
}

// Check is one security check.
type Check struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	State    string    `json:"state"`
	Findings []Finding `json:"findings"`
	Scanned  bool      `json:"scanned"`  // the lab's own audit ran in full and was read
	Isolated bool      `json:"isolated"` // this check cut the lab off
	host     hostStats
	alerted  bool // the owner was told about it already
}

// vmReport is lab-audit's output (deploy/lab/vm/lab-audit). Everything in it came from a machine
// that may not be trustworthy any more.
type vmReport struct {
	Version             int           `json:"version"`
	RebootRequiredSince int64         `json:"reboot_required_since"`
	Debsecan            []string      `json:"debsecan"`
	Images              []imageReport `json:"images"`
	Signals             []vmSignal    `json:"signals"`
	Errors              []string      `json:"errors"`
}

type imageReport struct {
	Image string   `json:"image"`
	Vulns []vuln   `json:"vulns"`
	CVEs  []string `json:"cves"`
	Error string   `json:"error"`
}

type vuln struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Package  string `json:"package"`
	Version  string `json:"version"`
	Fix      string `json:"fix"`
}

type vmSignal struct {
	Level  string `json:"level"` // strong or weak
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

// Job is the nightly security check. After a check that finds no sign of a break-in it takes the
// nightly snapshot, so the snapshots kept are from times the lab looked healthy.
func (s *Service) Job() jobs.Job {
	return jobs.Job{
		Name: JobName,
		Next: jobs.Daily(s.loc, checkCatchUp, func(context.Context) (jobs.Clock, bool) {
			return jobs.Clock{Hour: checkAt}, s.Configured()
		}),
		Run: func(ctx context.Context) (string, error) {
			c, note, err := s.runCheck(ctx, true)
			if errors.Is(err, errIsolated) {
				return "Lab: cut off from the network, not checked", nil
			}
			if err != nil {
				return "", err
			}
			return checkSummary(c) + note, nil
		},
		Timeout: checkTimeout,
	}
}

// errIsolated is a check skipped because the lab is cut off: it can't be reached, and the
// verdict that cut it off stands until the owner reconnects it.
var errIsolated = errors.New("the lab is cut off from the network, so it can't be checked until the owner reconnects it")

// runCheck checks the lab, records the result and acts on it. nightly also takes the nightly
// snapshot when the check found no sign of a break-in; note says how that went.
func (s *Service) runCheck(ctx context.Context, nightly bool) (Check, string, error) {
	if !s.Configured() {
		return Check{}, "", errNotSetUp
	}
	if !s.checking.TryLock() {
		return Check{}, "", errors.New("a security check of the lab is already running")
	}
	defer s.checking.Unlock()

	host, err := s.stats(ctx)
	if err != nil {
		return Check{}, "", err
	}
	if host.Isolated {
		return Check{}, "", errIsolated
	}
	if host.State != "running" {
		return Check{}, "", fmt.Errorf("the lab isn't running (%s)", host.State)
	}
	prev, err := s.lastCheck(ctx)
	if err != nil {
		return Check{}, "", err
	}
	lastScan, err := s.lastScan(ctx)
	if err != nil {
		return Check{}, "", err
	}

	rep, repErr := s.audit(ctx)
	kev, kevErr := s.knownExploited(ctx)
	now := s.now()
	c := Check{At: now, host: host, Scanned: repErr == nil && complete(rep)}
	c.Findings = evaluate(prev, host, rep, repErr, kev, now, lastScan)
	if kevErr != nil {
		c.Findings = append(c.Findings, Finding{Level: levelInfo, Key: "kev_unavailable",
			Text: "Couldn't get CISA's list of exploited vulnerabilities, so nothing was matched against it: " + short(kevErr.Error(), 150)})
	}
	c.State = stateOf(c.Findings)

	// A strong sign cuts the lab off at once: unplugging it is harmless and the owner can undo it.
	if c.State == StateCompromised {
		if err := s.isolate(ctx); err != nil {
			s.log.Error("lab: can't cut the lab off after a strong sign of a break-in", "error", err)
			c.Findings = append(c.Findings, Finding{Level: levelInfo, Key: "isolate_failed",
				Text: "Cutting the lab off failed: " + short(err.Error(), 150)})
		} else {
			c.Isolated = true
		}
	}
	if c.ID, err = s.saveCheck(ctx, c); err != nil {
		return Check{}, "", err
	}
	c.alerted = s.alert(ctx, prev, c)

	// Only a full check with no sign of a break-in earns a snapshot: two snapshots are all there
	// is, and an unchecked one could push out the last good one.
	note := ""
	if nightly && c.Scanned && rank[c.State] < rank[StateSuspicious] {
		if name, err := s.takeSnapshot(ctx, "nightly"); err != nil {
			s.log.Warn("lab: nightly snapshot failed", "error", err)
			note = "; the nightly snapshot failed: " + short(err.Error(), 150)
		} else {
			note = "; snapshot " + name
		}
	}
	return c, note, nil
}

// audit runs lab-audit on the lab and reads its report.
func (s *Service) audit(ctx context.Context) (*vmReport, error) {
	res, err := s.shell(ctx, "sudo -n /usr/local/sbin/lab-audit 2>/dev/null", nil, auditTimeout, auditMaxOut)
	if err != nil {
		return nil, err
	}
	switch {
	case res.TimedOut:
		return nil, errors.New("the lab's audit ran out of time")
	case res.Exit != 0:
		return nil, fmt.Errorf("the lab's audit failed (exit %d): %s", res.Exit, short(res.Output, maxVMText))
	case res.Cut > 0:
		return nil, errors.New("the lab's audit report was too long")
	}
	var rep vmReport
	if err := json.Unmarshal([]byte(res.Output), &rep); err != nil {
		return nil, fmt.Errorf("the lab's audit report was unreadable: %w", err)
	}
	if rep.Version != 1 {
		return nil, fmt.Errorf("the lab's audit report has version %d, not 1", rep.Version)
	}
	return &rep, nil
}

// complete reports whether every part of the lab's audit worked.
func complete(rep *vmReport) bool {
	if rep == nil || len(rep.Errors) > 0 {
		return false
	}
	for _, img := range rep.Images {
		if img.Error != "" {
			return false
		}
	}
	return true
}

// debsecanLine is one line of debsecan's summary: id, package, and notes like
// "remotely exploitable, high urgency".
var debsecanLine = regexp.MustCompile(`^(CVE-\d{4}-\d+|TEMP-\d+-\w+)\s+(\S+)(?:\s+\((.*)\))?\s*$`)

// evaluate turns what the host saw and what the lab reported into findings. prev is the last
// check (nil before the first); lastScan is when the lab's own audit last worked in full (zero:
// never). Not seeing all of the lab for longer than blindAfter is itself suspicious.
func evaluate(prev *Check, host hostStats, rep *vmReport, repErr error, kev map[string]bool, now, lastScan time.Time) []Finding {
	var out []Finding
	add := func(level, key, format string, args ...any) {
		out = append(out, Finding{Level: level, Key: key, Text: short(fmt.Sprintf(format, args...), maxFindingText)})
	}

	// What the host saw. Counters restart with the host's firewall: then the new count is all new.
	var before hostStats
	if prev != nil {
		before = prev.host
	}
	delta := func(cur, old uint64) uint64 {
		if cur >= old {
			return cur - old
		}
		return cur
	}
	if n := delta(host.SMTPBlocked, before.SMTPBlocked); n > 0 {
		add(StateCompromised, "smtp_out", "The lab tried to send mail straight to mail servers (port 25): the host's firewall blocked %d packets. That's what spam bots do.", n)
	}
	if n := delta(host.PrivateBlocked, before.PrivateBlocked) + delta(host.HostBlocked, before.HostBlocked); n >= blockedTCPAlarm {
		add(StateSuspicious, "private_tcp", "The lab kept trying to connect into the home network, the tailnet or the host (%d blocked packets); the firewall stopped them all.", n)
	}
	if before.At > 0 && host.VCPUs > 0 && host.At > before.At && host.CPUTimeNS >= before.CPUTimeNS {
		span := time.Duration(host.At-before.At) * time.Second
		busy := float64(host.CPUTimeNS-before.CPUTimeNS) / (float64(span.Nanoseconds()) * float64(host.VCPUs))
		if span >= busyMinSpan && busy > busyCPU {
			add(StateSuspicious, "cpu_busy", "The lab kept its CPUs %.0f%% busy on average over the last %s, which is what a hidden miner looks like.", busy*100, span.Round(time.Hour))
		}
	}

	blindLevel := levelInfo
	if lastScan.IsZero() || now.Sub(lastScan) > blindAfter {
		blindLevel = StateSuspicious
	}
	if repErr != nil {
		add(blindLevel, "audit_failed", "The check couldn't look inside the lab: %s", short(repErr.Error(), 150))
		return out
	}

	for _, sig := range rep.Signals {
		level := StateSuspicious
		if sig.Level == "strong" {
			level = StateCompromised
		}
		add(level, "sig:"+short(sig.Check, 40)+":"+short(sig.Detail, 80), "%s: %q", signalText(sig.Check), short(sig.Detail, maxVMText))
	}
	if !complete(rep) {
		var errs []string
		errs = append(errs, rep.Errors...)
		for _, img := range rep.Images {
			if img.Error != "" {
				errs = append(errs, short(img.Image, 60)+": "+img.Error)
			}
		}
		add(blindLevel, "audit_incomplete", "Parts of the check failed, so it didn't see everything: %q", short(strings.Join(errs, "; "), maxVMText))
	}
	if rep.RebootRequiredSince > 0 {
		since := time.Unix(rep.RebootRequiredSince, 0)
		if now.Sub(since) > rebootGrace {
			add(StateVulnerable, "reboot", "An installed update needs the lab restarted to take effect, waiting since %s.", since.UTC().Format("Jan 2"))
		}
	}

	// Debian packages: what's on the exploited list is urgent; remotely exploitable, high-urgency
	// fixes are worth installing; the rest is counted.
	var high, other []string
	for _, line := range rep.Debsecan {
		m := debsecanLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, pkg, notes := m[1], short(m[2], 60), m[3]
		switch {
		case kev[id]:
			add(StateUrgent, "kev:"+id, "%s in the Debian package %q is being exploited in the wild (CISA), and a fix is available.", id, pkg)
		case strings.Contains(notes, "remotely exploitable") && strings.Contains(notes, "high urgency"):
			high = append(high, pkg)
		default:
			other = append(other, pkg)
		}
	}
	if high = uniq(high); len(high) > 0 {
		add(StateVulnerable, "debian_high", "Fixes for remotely exploitable, high-urgency problems aren't installed yet in %d Debian package(s): %q.", len(high), list(high, 8))
	}
	if other = uniq(other); len(other) > 0 {
		add(levelInfo, "debian_other", "%d other Debian package(s) have published fixes not installed yet.", len(other))
	}

	// Containers.
	for _, img := range rep.Images {
		name := short(img.Image, 80)
		if img.Error != "" {
			continue // counted in audit_incomplete
		}
		for _, id := range uniq(img.CVEs) {
			if kev[id] {
				add(StateUrgent, "kev:"+id+":"+name, "%s in the container image %q is being exploited in the wild (CISA).", id, name)
			}
		}
		var critical []string
		highs := 0
		for _, v := range img.Vulns {
			switch {
			case v.Fix == "":
			case v.Severity == "Critical":
				critical = append(critical, fmt.Sprintf("%s in %s (fixed in %s)", short(v.ID, 30), short(v.Package, 40), short(v.Fix, 30)))
			case v.Severity == "High":
				highs++
			}
		}
		if len(critical) > 0 {
			add(StateVulnerable, "critical:"+name, "The image %q has %d critical problem(s) with fixes available, e.g. %q. Pulling a newer image usually fixes them.", name, len(critical), list(critical, 3))
		}
		if highs > 0 {
			add(levelInfo, "high:"+name, "The image %q has %d high-severity problem(s) with fixes available.", name, highs)
		}
	}
	return out
}

// signalText says what one of lab-audit's checks found, in words.
func signalText(check string) string {
	switch check {
	case "modified_system_file":
		return "A packaged program or library was changed on disk"
	case "exec_from_shm":
		return "A program is running from shared memory (/dev/shm)"
	case "exec_deleted_from_tmp":
		return "A program is running from a temporary folder it was deleted from"
	case "exec_from_tmp":
		return "A program is running from a temporary folder"
	case "extra_root_account":
		return "An account other than root has root's powers"
	case "ssh_keys_changed":
		return "The keys allowed to log in changed"
	case "stray_authorized_keys":
		return "Someone added login keys where SSH doesn't read them"
	case "unknown_ssh_login":
		return "Someone logged in with a key that isn't Hekate's or the owner's"
	}
	return "The lab's audit flagged " + short(check, 40)
}

// stateOf is the worst level among the findings.
func stateOf(fs []Finding) string {
	state := StateClean
	for _, f := range fs {
		if rank[f.Level] > rank[state] {
			state = f.Level
		}
	}
	return state
}

// alert tells the owner about what got worse since the last check, a finding that's new or more
// severe than before: a strong sign of a break-in urgently, a weak one or a known-exploited
// vulnerability normally. Vulnerabilities with fixes waiting only show in her context, for her
// to bring up.
func (s *Service) alert(ctx context.Context, prev *Check, c Check) bool {
	known := map[string]int{}
	if prev != nil {
		for _, f := range prev.Findings {
			known[f.Key] = max(known[f.Key], rank[f.Level]+1)
		}
	}
	var fresh []Finding
	for _, f := range c.Findings {
		if rank[f.Level] >= rank[StateUrgent] && rank[f.Level]+1 > known[f.Key] {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return false
	}
	sortFindings(fresh)
	title, urgency := "Lab: a vulnerability that's being exploited", push.UrgencyNormal
	switch {
	case c.State == StateCompromised && c.Isolated:
		title, urgency = "Lab cut off: signs of a break-in", push.UrgencyHigh
	case c.State == StateCompromised:
		title, urgency = "Lab: signs of a break-in", push.UrgencyHigh
	case c.State == StateSuspicious:
		title = "Lab: something looks off"
	}
	body := fresh[0].Text
	if len(fresh) > 1 {
		body += fmt.Sprintf(" (+%d more)", len(fresh)-1)
	}
	s.tell(ctx, fmt.Sprintf("lab-%d", c.ID), title, body, urgency)
	return true
}

// announceCheck tells the owner how a check they asked for went, unless an alert already did.
func (s *Service) announceCheck(ctx context.Context, c Check, err error) {
	switch {
	case errors.Is(err, errIsolated):
		s.tell(ctx, "lab-check", "Lab check: not run", "The lab is cut off from the network, so it can't be checked.", push.UrgencyNormal)
	case err != nil:
		s.tell(ctx, "lab-check", "Lab check failed", short(err.Error(), 200), push.UrgencyNormal)
	case !c.alerted:
		s.tell(ctx, fmt.Sprintf("lab-%d", c.ID), "Lab check: "+c.State, strings.TrimSuffix(stateWords(c.State), "."), push.UrgencyNormal)
	}
}

// tell notifies the owner in the app and by push.
func (s *Service) tell(ctx context.Context, id, title, body string, urgency push.Urgency) {
	s.events.Notify(proto.NewNotify(id, "nudge", title, body, nil))
	if s.pusher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := s.pusher.Send(pctx, push.Payload{Title: title, Body: body, URL: "/", Tag: id}, urgency); err != nil {
		s.log.Warn("lab: push failed", "error", err)
	}
}

// checkSummary is a check in one line, for the routine's record.
func checkSummary(c Check) string {
	counts := map[string]int{}
	for _, f := range c.Findings {
		counts[f.Level]++
	}
	sum := "Lab: " + c.State
	var parts []string
	for _, l := range []string{StateCompromised, StateSuspicious, StateUrgent, StateVulnerable} {
		if counts[l] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[l], l))
		}
	}
	if len(parts) > 0 {
		sum += " (" + strings.Join(parts, ", ") + ")"
	}
	if c.Isolated {
		sum += "; cut off from the network"
	}
	return sum
}

func (s *Service) saveCheck(ctx context.Context, c Check) (int64, error) {
	findings, err := json.Marshal(c.Findings)
	if err != nil {
		return 0, fmt.Errorf("encode findings: %w", err)
	}
	host, err := json.Marshal(c.host)
	if err != nil {
		return 0, fmt.Errorf("encode host stats: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lab_checks WHERE created_at < ?`, stamp(c.At.Add(-keepChecks))); err != nil {
		return 0, fmt.Errorf("prune lab checks: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO lab_checks (created_at, state, findings, host, scanned, isolated) VALUES (?, ?, ?, ?, ?, ?)`,
		stamp(c.At), c.State, string(findings), string(host), c.Scanned, c.Isolated)
	if err != nil {
		return 0, fmt.Errorf("save lab check: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("save lab check: %w", err)
	}
	return id, nil
}

// lastCheck is the latest check, or nil.
func (s *Service) lastCheck(ctx context.Context) (*Check, error) {
	var (
		c                  Check
		at, findings, host string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, created_at, state, findings, host, scanned, isolated FROM lab_checks ORDER BY id DESC LIMIT 1`).
		Scan(&c.ID, &at, &c.State, &findings, &host, &c.Scanned, &c.Isolated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no check yet
	}
	if err != nil {
		return nil, fmt.Errorf("read the last lab check: %w", err)
	}
	if c.At, err = time.Parse(time.RFC3339, at); err != nil {
		return nil, fmt.Errorf("read the last lab check: %w", err)
	}
	if err := json.Unmarshal([]byte(findings), &c.Findings); err != nil {
		return nil, fmt.Errorf("read the last lab check's findings: %w", err)
	}
	if err := json.Unmarshal([]byte(host), &c.host); err != nil {
		return nil, fmt.Errorf("read the last lab check's host stats: %w", err)
	}
	return &c, nil
}

// lastScan is when the lab's own audit last worked, or zero.
func (s *Service) lastScan(ctx context.Context) (time.Time, error) {
	var at sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT max(created_at) FROM lab_checks WHERE scanned = 1`).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("read the last lab scan: %w", err)
	}
	if !at.Valid {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, at.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the last lab scan: %w", err)
	}
	return t, nil
}

// short flattens text to one line of at most n runes.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func uniq(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return slices.Compact(out)
}

// list joins the first n items, saying how many more there are.
func list(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}
