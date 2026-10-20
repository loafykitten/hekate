package reflection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/memory"
)

// Change actions.
const (
	ActionSave   = "save"
	ActionUpdate = "update"
	ActionRetire = "retire"
)

// apply carries out a plan through the memory service, recording each change
// in the same transaction so it can be undone, then writes the diary entry and
// marks the reflection finished (the next one reads on from where it stopped).
// Each change applies only to the memory as the model saw it: one the owner
// edited meanwhile is left alone, as are changes the memory service refuses (an
// unknown id, a bad kind or date). Merges are an update plus retirements, so if
// an update to a known memory doesn't happen, nothing is retired: the
// information stays live.
// Once changes start, cancellation waits until they're done.
func (s *Service) apply(ctx context.Context, in *input, p plan) (string, error) {
	ctx = context.WithoutCancel(ctx)
	res, err := s.db.ExecContext(ctx, `INSERT INTO reflections (day, window_start, window_end, summary, created_at) VALUES (?, ?, ?, ?, ?)`,
		in.day, stamp(in.start), stamp(in.end), p.Summary, stamp(s.now()))
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	rid, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	var saved, updated, retired, refused, stale, held int
	updateFailed := false // an update to a memory she knows didn't happen
	recorder := func(action, reason string) memory.Recorder {
		return func(ctx context.Context, tx *sql.Tx, before, after *memory.Memory) error {
			id := ""
			b, a := "", ""
			if before != nil {
				raw, _ := json.Marshal(before)
				b, id = string(raw), before.ID
			}
			if after != nil {
				raw, _ := json.Marshal(after)
				a, id = string(raw), after.ID
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO reflection_changes (reflection_id, action, memory_id, before, after, reason)
				VALUES (?, ?, ?, ?, ?, ?)`, rid, action, id, b, a, clip(oneLine(reason), maxReason))
			if err != nil {
				return fmt.Errorf("record a reflection change: %w", err)
			}
			return nil
		}
	}
	// latest is each memory as it stands after this plan's own changes.
	latest := make(map[int64]memory.Memory, len(in.byID))
	for id, m := range in.byID {
		latest[id] = m
	}
	failed := func(what string, id int64, err error) {
		if errors.Is(err, memory.ErrChanged) || errors.Is(err, memory.ErrExists) {
			stale++
			return
		}
		s.log.Warn("reflection: a change was refused", "change", what, "memory_id", id, "error", err)
		refused++
	}
	update := func(id int64, patch memory.Patch, reason string) {
		before := latest[id]
		after, err := s.mem.UpdateIfUnchanged(ctx, id, before, patch, recorder(ActionUpdate, reason))
		if err != nil {
			failed(ActionUpdate, id, err)
			updateFailed = true
			return
		}
		latest[id] = after
		updated++
	}

	for _, u := range p.Update {
		id := int64(u.ID)
		patch := memory.Patch{Content: u.Content, Kind: u.Kind, Importance: u.Importance, FollowUpAt: u.FollowUpAt}
		if _, ok := latest[id]; !ok || (patch == memory.Patch{}) {
			refused++
			continue
		}
		update(id, patch, u.Reason)
	}
	for _, r := range p.Retire {
		id := int64(r.ID)
		before, ok := latest[id]
		switch {
		case !ok:
			refused++
			continue
		case updateFailed:
			held++ // it may be half of a merge whose other half didn't happen
			continue
		}
		if _, err := s.mem.RetireIfUnchanged(ctx, id, before, recorder(ActionRetire, r.Reason)); err != nil {
			failed(ActionRetire, id, err)
			continue
		}
		delete(latest, id)
		retired++
	}
	for _, sv := range p.Save {
		// Saving what she already remembers refreshes that memory: make it an update.
		if id, ok := findContent(latest, sv.Content); ok {
			cur := latest[id]
			patch := memory.Patch{}
			if sv.Importance > cur.Importance {
				patch.Importance = &sv.Importance
			}
			if sv.FollowUpAt != "" {
				patch.FollowUpAt = &sv.FollowUpAt
			}
			if patch != (memory.Patch{}) {
				update(id, patch, sv.Reason)
			}
			continue
		}
		m, err := s.mem.Insert(ctx, memory.NewMemory{Kind: sv.Kind, Content: sv.Content, Importance: sv.Importance, FollowUpAt: sv.FollowUpAt},
			recorder(ActionSave, sv.Reason))
		if err != nil {
			failed(ActionSave, 0, err)
			continue
		}
		id, _ := strconv.ParseInt(m.ID, 10, 64)
		latest[id] = m
		saved++
	}
	if err := s.writeDiary(ctx, in.day, p.Diary); err != nil {
		return "", err
	}

	summary := fmt.Sprintf("Saved %d, updated %d and retired %d memories; wrote the diary for %s", saved, updated, retired, in.day)
	if refused > 0 {
		summary += fmt.Sprintf(" (%d changes refused)", refused)
	}
	if stale > 0 {
		summary += fmt.Sprintf(" (%d left alone: changed meanwhile)", stale)
	}
	if held > 0 {
		summary += fmt.Sprintf(" (%d retirements held back: an update didn't happen)", held)
	}
	stored := p.Summary
	if stored == "" {
		stored = summary
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE reflections SET summary = ?, finished_at = ? WHERE id = ?`, stored, stamp(s.now()), rid); err != nil {
		return "", fmt.Errorf("finish the reflection: %w", err)
	}
	s.events.Invalidate(DomainJournal)
	return summary, nil
}

// findContent finds a live memory with the same text, any case.
func findContent(mems map[int64]memory.Memory, content string) (int64, bool) {
	want := strings.ToLower(strings.TrimSpace(content))
	for id, m := range mems {
		if strings.ToLower(strings.TrimSpace(m.Content)) == want {
			return id, true
		}
	}
	return 0, false
}

// writeDiary stores the day's entry, replacing one written earlier that day.
func (s *Service) writeDiary(ctx context.Context, day, text string) error {
	now := stamp(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO diary (day, text, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at`, day, text, now, now)
	if err != nil {
		return fmt.Errorf("write the diary: %w", err)
	}
	return nil
}
