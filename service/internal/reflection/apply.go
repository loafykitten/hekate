package reflection

import (
	"context"
	"encoding/json"
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
// that took effect so it can be undone, then writes the diary entry. Changes
// the memory service refuses (an unknown id, a bad kind or date) are skipped.
func (s *Service) apply(ctx context.Context, in *input, p plan) (string, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO reflections (day, window_start, window_end, summary, created_at) VALUES (?, ?, ?, ?, ?)`,
		in.day, stamp(in.start), stamp(in.end), p.Summary, stamp(s.now()))
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	rid, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	var saved, updated, retired, skipped int
	record := func(action string, id int64, before, after *memory.Memory, reason string) error {
		b, a := "", ""
		if before != nil {
			raw, _ := json.Marshal(before)
			b = string(raw)
		}
		if after != nil {
			raw, _ := json.Marshal(after)
			a = string(raw)
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO reflection_changes (reflection_id, action, memory_id, before, after, reason)
			VALUES (?, ?, ?, ?, ?, ?)`, rid, action, id, b, a, clip(oneLine(reason), maxReason))
		if err != nil {
			return fmt.Errorf("record a reflection change: %w", err)
		}
		return nil
	}

	for _, u := range p.Update {
		id := int64(u.ID)
		before, ok := in.byID[id]
		patch := memory.Patch{Content: u.Content, Kind: u.Kind, Importance: u.Importance, FollowUpAt: u.FollowUpAt}
		if !ok || (patch == memory.Patch{}) {
			skipped++
			continue
		}
		after, err := s.mem.Update(ctx, id, patch)
		if err != nil {
			s.log.Warn("reflection: an update was refused", "memory_id", id, "error", err)
			skipped++
			continue
		}
		if same(before, after) {
			continue
		}
		if err := record(ActionUpdate, id, &before, &after, u.Reason); err != nil {
			return "", err
		}
		updated++
	}
	for _, r := range p.Retire {
		id := int64(r.ID)
		before, ok := in.byID[id]
		if !ok {
			skipped++
			continue
		}
		cur, err := s.mem.Retire(ctx, id)
		if err != nil {
			s.log.Warn("reflection: a retirement was refused", "memory_id", id, "error", err)
			skipped++
			continue
		}
		if !same(before, cur) {
			before = cur // updated just above: undoing the retirement brings back this version
		}
		if err := record(ActionRetire, id, &before, nil, r.Reason); err != nil {
			return "", err
		}
		retired++
	}
	for _, sv := range p.Save {
		m, existed, err := s.mem.Save(ctx, memory.NewMemory{Kind: sv.Kind, Content: sv.Content, Importance: sv.Importance, FollowUpAt: sv.FollowUpAt})
		if err != nil {
			s.log.Warn("reflection: a save was refused", "error", err)
			skipped++
			continue
		}
		id, _ := strconv.ParseInt(m.ID, 10, 64)
		if existed {
			// Saving known text refreshes it (importance, follow-up): an update.
			if before, ok := in.byID[id]; ok && !same(before, m) {
				if err := record(ActionUpdate, id, &before, &m, sv.Reason); err != nil {
					return "", err
				}
				updated++
			}
			continue
		}
		if err := record(ActionSave, id, nil, &m, sv.Reason); err != nil {
			return "", err
		}
		saved++
	}
	if err := s.writeDiary(ctx, in.day, p.Diary); err != nil {
		return "", err
	}

	summary := fmt.Sprintf("Saved %d, updated %d and retired %d memories; wrote the diary for %s", saved, updated, retired, in.day)
	if skipped > 0 {
		summary += fmt.Sprintf(" (%d changes refused)", skipped)
	}
	if p.Summary == "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE reflections SET summary = ? WHERE id = ?`, summary, rid); err != nil {
			return "", fmt.Errorf("record the reflection: %w", err)
		}
	}
	s.events.Invalidate(DomainJournal)
	return summary, nil
}

// same reports whether two snapshots of a memory hold the same fields.
func same(a, b memory.Memory) bool {
	fa, fb := "", ""
	if a.FollowUpAt != nil {
		fa = *a.FollowUpAt
	}
	if b.FollowUpAt != nil {
		fb = *b.FollowUpAt
	}
	return a.Kind == b.Kind && strings.TrimSpace(a.Content) == strings.TrimSpace(b.Content) && a.Importance == b.Importance && fa == fb
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
