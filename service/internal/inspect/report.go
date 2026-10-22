package inspect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/logging"
)

// Limits on what the tools read and return.
const (
	maxLogLines    = 50
	maxLogLine     = 300
	maxRows        = 50
	deployTail     = 256 << 10 // bytes of the deploy log read from its end
	maxDeployField = 200
)

// Status is self_status: version and uptime, each check, the routines' last
// runs, warnings logged lately and the last deploy.
func (s *Service) Status(ctx context.Context) (string, error) {
	now := s.now()
	var b strings.Builder
	fmt.Fprintf(&b, "Hekate %s, running since %s (%s).\n", s.version, s.local(s.started), roundDur(now.Sub(s.started)))
	for _, c := range s.checks {
		ok, detail := c.Run(ctx)
		mark := "ok"
		if !ok {
			mark = "NOT OK"
		}
		fmt.Fprintf(&b, "- %s: %s", c.Name, mark)
		if detail != "" {
			fmt.Fprintf(&b, ", %s", oneLine(detail))
		}
		b.WriteString("\n")
	}
	if s.jobs != nil {
		latest, err := s.jobs.Latest(ctx)
		if err != nil {
			return "", err
		}
		for _, name := range s.jobs.Names() {
			fmt.Fprintf(&b, "- Routine %s: %s%s\n", name, s.describeRun(latest[name]), s.freshness(ctx, name, latest[name], now))
		}
	}
	if s.ring != nil {
		warns := s.ring.Query(logging.Query{MinLevel: slog.LevelWarn, Since: now.Add(-24 * time.Hour)})
		errs := 0
		for _, r := range warns {
			switch {
			case r.Level >= slog.LevelError && clientCancelled(r):
				// an older log line: a client hung up, nothing failed here
			case r.Level >= slog.LevelError:
				errs++
			}
		}
		warns = slices.DeleteFunc(slices.Clone(warns), func(r logging.Record) bool { return r.Level >= slog.LevelError && clientCancelled(r) })
		fmt.Fprintf(&b, "- Logs, last 24 h (since the last restart at most): %d errors, %d warnings. self_logs shows them.\n", errs, len(warns)-errs)
	}
	if s.deployLog != "" {
		entries, err := s.deploys(1)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "- Deploys: can't read the deploy log (%s)\n", oneLine(err.Error()))
		case len(entries) == 0:
			b.WriteString("- Deploys: none recorded\n")
		default:
			fmt.Fprintf(&b, "- Last deploy event: %s\n", s.describeDeploy(entries[0]))
		}
	}
	return b.String(), nil
}

// clientCancelled is a request that failed only because its client went away.
func clientCancelled(r logging.Record) bool {
	return r.Message == "request failed" && strings.Contains(r.Attrs["error"], "context canceled")
}

// overdueGrace is how far past its due time a routine may start before it
// counts as overdue: a run can take up to the runner's ten-minute timeout.
const overdueGrace = 15 * time.Minute

// freshness says how long ago a routine last succeeded, and flags it when its
// own schedule says it should have run by now and it hasn't. A routine that is
// switched off, or sleeping outside its hours, is never flagged.
func (s *Service) freshness(ctx context.Context, name string, latest *jobs.Run, now time.Time) string {
	if latest == nil {
		return ""
	}
	runs, err := s.jobs.Runs(ctx, name, 12)
	if err != nil {
		return ""
	}
	var lastOK string
	for _, r := range runs { // newest first
		if r.Status == jobs.StatusOK {
			lastOK = r.StartedAt
			if r.FinishedAt != nil {
				lastOK = *r.FinishedAt
			}
			break
		}
	}
	out := "; never succeeded in its recorded runs"
	if t, err := time.Parse(time.RFC3339, lastOK); err == nil {
		out = "; last success " + roundDur(now.Sub(t)) + " ago"
	}
	if next, ok := s.jobs.Due(ctx, name); ok && now.Sub(next) > overdueGrace {
		out += " -- OVERDUE (was due " + s.local(next) + ")"
	}
	return out
}

func (s *Service) describeRun(run *jobs.Run) string {
	if run == nil {
		return "hasn't run yet"
	}
	out := fmt.Sprintf("%s at %s", run.Status, s.localStamp(run.StartedAt))
	if run.Summary != "" {
		out += ": " + oneLine(run.Summary)
	}
	if run.Error != "" {
		out += " (" + oneLine(run.Error) + ")"
	}
	return out
}

// LogQuery is self_logs' arguments.
type LogQuery struct {
	Level        string `json:"level"`         // info, warn (default) or error
	SinceMinutes int    `json:"since_minutes"` // default 1440
	Contains     string `json:"contains"`
	Limit        int    `json:"limit"` // default 20
}

// Logs is self_logs: recent log records at or above a level, newest first.
// The log ring only holds what was logged since the last restart.
func (s *Service) Logs(q LogQuery) (string, error) {
	if s.ring == nil {
		return "No logs are available.", nil
	}
	level := slog.LevelWarn
	switch strings.ToLower(q.Level) {
	case "", "warn", "warning":
	case "info":
		level = slog.LevelInfo
	case "error":
		level = slog.LevelError
	default:
		return "", fmt.Errorf("level must be info, warn or error")
	}
	since := q.SinceMinutes
	if since <= 0 {
		since = 24 * 60
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, maxLogLines)
	recs := s.ring.Query(logging.Query{MinLevel: level, Since: s.now().Add(-time.Duration(since) * time.Minute),
		Substring: q.Contains, Limit: limit})
	if len(recs) == 0 {
		return fmt.Sprintf("Nothing at %s or above in the last %s.", strings.ToLower(level.String()), roundDur(time.Duration(since)*time.Minute)), nil
	}
	var b strings.Builder
	for _, r := range recs {
		line := fmt.Sprintf("%s %s %s", s.local(r.Time), r.Level, r.Message)
		keys := make([]string, 0, len(r.Attrs))
		for k := range r.Attrs {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			line += fmt.Sprintf(" %s=%s", k, r.Attrs[k])
		}
		b.WriteString(clip(oneLine(line), maxLogLine))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// Usage is self_usage: requests, tokens and cost over the last days, for
// conversation by model and for background work by source.
func (s *Service) Usage(ctx context.Context, days int) (string, error) {
	if days <= 0 {
		days = 7
	}
	days = min(days, 90)
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	var b strings.Builder
	fmt.Fprintf(&b, "Last %d day(s):\n", days)
	total, unpriced := 0.0, 0
	add := func(label, query string) error {
		rows, err := s.db.QueryContext(ctx, query, since)
		if err != nil {
			return fmt.Errorf("usage: %w", err)
		}
		defer func() { _ = rows.Close() }()
		found := false
		for rows.Next() {
			var name string
			var n, in, out, missing int
			var cost float64
			if err := rows.Scan(&name, &n, &in, &out, &cost, &missing); err != nil {
				return fmt.Errorf("usage: %w", err)
			}
			if !found {
				fmt.Fprintf(&b, "%s:\n", label)
				found = true
			}
			fmt.Fprintf(&b, "- %s: %d requests, %d tokens in, %d out, $%.4f\n", name, n, in, out, cost)
			total += cost
			unpriced += missing
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("usage: %w", err)
		}
		if !found {
			fmt.Fprintf(&b, "%s: none\n", label)
		}
		return nil
	}
	if err := add("Conversation, by model", `SELECT model, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		COALESCE(SUM(cost_usd), 0), SUM(cost_usd IS NULL) FROM usage WHERE created_at >= ? GROUP BY model ORDER BY 5 DESC`); err != nil {
		return "", err
	}
	if err := add("Background work, by source", `SELECT source, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		COALESCE(SUM(cost_usd), 0), SUM(cost_usd IS NULL) FROM background_usage WHERE created_at >= ? GROUP BY source ORDER BY 5 DESC`); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "Total: $%.4f", total)
	if unpriced > 0 {
		fmt.Fprintf(&b, " (%d requests had no reported cost)", unpriced)
	}
	b.WriteString("\n")
	return b.String(), nil
}

// ActivityQuery is self_activity's arguments.
type ActivityQuery struct {
	Kind   string `json:"kind"`   // tools (default), routines or deploys
	Hours  int    `json:"hours"`  // default 24
	Tool   string `json:"tool"`   // tools: only this tool
	Status string `json:"status"` // tools: ok or error
	Limit  int    `json:"limit"`  // default 20
}

// Activity is self_activity: her recent tool calls, routine runs or deploys, newest first.
func (s *Service) Activity(ctx context.Context, q ActivityQuery) (string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, maxRows)
	hours := q.Hours
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, 30*24)
	since := s.now().Add(-time.Duration(hours) * time.Hour)
	switch q.Kind {
	case "", "tools":
		return s.toolActivity(ctx, q, since, limit)
	case "routines":
		if s.jobs == nil {
			return "No routines are set up.", nil
		}
		runs, err := s.jobs.Runs(ctx, "", limit)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, r := range runs {
			if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil && t.Before(since) {
				break
			}
			fmt.Fprintf(&b, "- %s: %s\n", r.Job, s.describeRun(&r))
		}
		if b.Len() == 0 {
			return "No routine ran in that time.", nil
		}
		return b.String(), nil
	case "deploys":
		if s.deployLog == "" {
			return "The deploy log isn't available here.", nil
		}
		entries, err := s.deploys(limit)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, e := range entries {
			if !e.at.IsZero() && e.at.Before(since) {
				break
			}
			fmt.Fprintf(&b, "- %s\n", s.describeDeploy(e))
		}
		if b.Len() == 0 {
			return "No deploy events in that time.", nil
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("kind must be tools, routines or deploys")
	}
}

func (s *Service) toolActivity(ctx context.Context, q ActivityQuery, since time.Time, limit int) (string, error) {
	if q.Status != "" && q.Status != "ok" && q.Status != "error" {
		return "", fmt.Errorf("status must be ok or error")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tool, status, result, duration_ms, created_at FROM activity
		WHERE created_at >= ? AND (? = '' OR tool = ?) AND (? = '' OR status = ?)
		ORDER BY id DESC LIMIT ?`, since.UTC().Format(time.RFC3339Nano), q.Tool, q.Tool, q.Status, q.Status, limit)
	if err != nil {
		return "", fmt.Errorf("activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var tool, status, result, at string
		var ms int64
		if err := rows.Scan(&tool, &status, &result, &ms, &at); err != nil {
			return "", fmt.Errorf("activity: %w", err)
		}
		fmt.Fprintf(&b, "- %s %s %s (%d ms)", s.localStamp(at), tool, status, ms)
		if result != "" {
			fmt.Fprintf(&b, ": %s", clip(oneLine(result), 160))
		}
		b.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("activity: %w", err)
	}
	if b.Len() == 0 {
		return "No tool calls in that time.", nil
	}
	return b.String(), nil
}

// deployEntry is one line of the deployer's log.
type deployEntry struct {
	at     time.Time
	event  string
	fields map[string]string
}

// deploys reads the newest n entries of the deploy log, newest first.
func (s *Service) deploys(n int) ([]deployEntry, error) {
	f, err := os.Open(s.deployLog)
	if err != nil {
		return nil, fmt.Errorf("open the deploy log: %w", err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > deployTail {
		if _, err := f.Seek(-deployTail, io.SeekEnd); err != nil {
			return nil, fmt.Errorf("read the deploy log: %w", err)
		}
	}
	var all []deployEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var raw map[string]any
		if json.Unmarshal(sc.Bytes(), &raw) != nil {
			continue // the first line after seeking is usually partial
		}
		e := deployEntry{fields: map[string]string{}}
		for k, v := range raw {
			str := fmt.Sprint(v)
			switch k {
			case "ts":
				e.at, _ = time.Parse(time.RFC3339, str)
			case "event":
				e.event = str
			default:
				e.fields[k] = str
			}
		}
		all = append(all, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read the deploy log: %w", err)
	}
	slices.Reverse(all)
	return all[:min(n, len(all))], nil
}

func (s *Service) describeDeploy(e deployEntry) string {
	out := e.event
	if !e.at.IsZero() {
		out = s.local(e.at) + " " + out
	}
	keys := make([]string, 0, len(e.fields))
	for k := range e.fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := e.fields[k]
		if k == "commit" && len(v) > 12 {
			v = v[:12]
		}
		out += fmt.Sprintf(" %s=%s", k, clip(oneLine(v), maxDeployField))
	}
	return out
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
