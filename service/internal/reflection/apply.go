package reflection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
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
// any update doesn't happen, nothing is retired: the information stays live. If
// the database fails, or the owner forgets a memory while she's thinking (the
// plan may carry its words into another memory), the reflection stops
// unfinished and the next one reads the same conversation again.
// Once changes start, cancellation waits until they're done.
func (s *Service) apply(ctx context.Context, in *input, p plan, forgets int64) (string, error) {
	ctx = context.WithoutCancel(ctx)
	res, err := s.db.ExecContext(ctx, `INSERT INTO reflections (day, window_start, window_end, created_at) VALUES (?, ?, ?, ?)`,
		in.day, stamp(in.start), stamp(in.end), stamp(s.now()))
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	rid, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("record the reflection: %w", err)
	}
	var saved, updated, retired, refused, stale, held, forgottenHeld, ungrounded int
	updateFailed := p.updatesCut // an update in the plan didn't happen
	// also runs after the change is recorded, with its id.
	recorder := func(action, reason, mergedFrom string, also ...func(tx *sql.Tx, changeID int64) error) memory.Recorder {
		return func(ctx context.Context, tx *sql.Tx, before, after *memory.Memory) error {
			if s.mem.Forgets() != forgets {
				return errForgotten
			}
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
			res, err := tx.ExecContext(ctx, `INSERT INTO reflection_changes (reflection_id, action, memory_id, before, after, reason, merged_from)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, rid, action, id, b, a, clip(oneLine(reason), maxReason), mergedFrom)
			if err != nil {
				return fmt.Errorf("record a reflection change: %w", err)
			}
			changeID, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("record a reflection change: %w", err)
			}
			for _, f := range also {
				if err := f(tx, changeID); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// latest is each memory as it stands after this plan's own changes.
	latest := make(map[int64]memory.Memory, len(in.byID))
	for id, m := range in.byID {
		latest[id] = m
	}
	// failed sorts an error from the memory service: a memory changed meanwhile
	// or a change it refuses (bad input, unknown id) is skipped; anything else
	// (the database failing) stops the reflection unfinished.
	failed := func(what string, id int64, err error) error {
		var he *httpx.Error
		switch {
		case errors.Is(err, memory.ErrChanged), errors.Is(err, memory.ErrExists):
			stale++
		case errors.Is(err, memory.ErrForgotten):
			forgottenHeld++
		case errors.As(err, &he) && he.Status < 500:
			s.log.Warn("reflection: a change was refused", "change", what, "memory_id", id, "error", err)
			refused++
		case errors.Is(err, errForgotten):
			return errors.New("a memory was forgotten while she reflected; the next reflection reads this conversation again")
		default:
			return fmt.Errorf("reflection: %s memory %d: %w", what, id, err)
		}
		return nil
	}
	// update applies one update. Merged sources are linked in the same
	// transaction, so forgetting one of them later forgets this one too.
	update := func(id int64, patch memory.Patch, reason string, mergedFrom []int64) error {
		before := latest[id]
		link := func(tx *sql.Tx, changeID int64) error {
			// The sources must still say what she read: an edit made while she
			// thought would otherwise be merged in its old form.
			for _, src := range mergedFrom {
				var content string
				err := tx.QueryRowContext(ctx, `SELECT content FROM memories WHERE id = ? AND deleted_at IS NULL`, src).Scan(&content)
				if errors.Is(err, sql.ErrNoRows) || (err == nil && content != latest[src].Content) {
					return memory.ErrChanged
				}
				if err != nil {
					return fmt.Errorf("check a merge source: %w", err)
				}
			}
			for _, src := range mergedFrom {
				// A merge back the other way (this memory already merged, however
				// indirectly, into the source) would make a cycle no undo can unwind.
				var cycle int
				err := tx.QueryRowContext(ctx, `WITH RECURSIVE onward(id) AS (
						SELECT ? UNION SELECT mm.into_id FROM memory_merges mm JOIN onward ON mm.source_id = onward.id
					) SELECT COUNT(*) FROM onward WHERE id = ?`, id, src).Scan(&cycle)
				if err != nil {
					return fmt.Errorf("check a merge: %w", err)
				}
				if cycle > 0 {
					return httpx.BadRequest("memory %d was already merged into memory %d", id, src)
				}
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_merges (source_id, into_id, change_id) VALUES (?, ?, ?)`, src, id, changeID); err != nil {
					return fmt.Errorf("record a merge: %w", err)
				}
			}
			return nil
		}
		ids := make([]string, len(mergedFrom))
		for i, src := range mergedFrom {
			ids[i] = strconv.FormatInt(src, 10)
		}
		after, err := s.mem.UpdateIfUnchanged(ctx, id, before, patch, recorder(ActionUpdate, reason, strings.Join(ids, ","), link))
		if err != nil {
			updateFailed = true
			return failed(ActionUpdate, id, err)
		}
		latest[id] = after
		updated++
		return nil
	}

	for _, u := range p.Update {
		id := int64(u.ID)
		patch := memory.Patch{Content: u.Content, Kind: u.Kind, Importance: u.Importance, FollowUpAt: u.FollowUpAt}
		var sources []int64
		valid := true
		for _, m := range u.MergedFrom {
			src := int64(m)
			if _, ok := latest[src]; !ok || src == id {
				valid = false // a merge from something she doesn't know, or from itself
			}
			sources = append(sources, src)
		}
		if _, ok := latest[id]; !ok || !valid || (patch == memory.Patch{}) {
			refused++
			updateFailed = true // a merge's destination may be the one that's wrong
			continue
		}
		// New wording must rest on a turn; a merge only rearranges what she holds.
		if _, ok := grounded(in, u.SourceTurn, u.Content != nil && len(sources) == 0); !ok {
			s.log.Warn("reflection: an update was skipped: it cites no turn from this conversation", "memory_id", id)
			ungrounded++
			updateFailed = true
			continue
		}
		if err := update(id, patch, u.Reason, sources); err != nil {
			return "", err
		}
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
		if _, err := s.mem.RetireIfUnchanged(ctx, id, before, recorder(ActionRetire, r.Reason, "")); err != nil {
			if err := failed(ActionRetire, id, err); err != nil {
				return "", err
			}
			continue
		}
		delete(latest, id)
		retired++
	}
	for _, sv := range p.Save {
		src, ok := grounded(in, sv.SourceTurn, true)
		if !ok {
			s.log.Warn("reflection: a save was skipped: it cites no turn from this conversation", "kind", sv.Kind)
			ungrounded++
			continue
		}
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
				if err := update(id, patch, sv.Reason, nil); err != nil {
					return "", err
				}
			}
			continue
		}
		m, err := s.mem.Insert(ctx, memory.NewMemory{Kind: sv.Kind, Content: sv.Content, Importance: sv.Importance, FollowUpAt: sv.FollowUpAt, SourceTurn: src},
			recorder(ActionSave, sv.Reason, ""))
		if err != nil {
			if err := failed(ActionSave, 0, err); err != nil {
				return "", err
			}
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
	if ungrounded > 0 {
		summary += fmt.Sprintf(" (%d changes skipped: no source turn from this conversation)", ungrounded)
	}
	if stale > 0 {
		summary += fmt.Sprintf(" (%d left alone: changed meanwhile)", stale)
	}
	if forgottenHeld > 0 {
		summary += fmt.Sprintf(" (%d not saved: the owner had them forgotten)", forgottenHeld)
	}
	if held > 0 {
		summary += fmt.Sprintf(" (%d retirements held back: an update didn't happen)", held)
	}
	// The stored summary is only counts: the model's own words could repeat a
	// fact that is forgotten later.
	if _, err := s.db.ExecContext(ctx, `UPDATE reflections SET summary = ?, finished_at = ? WHERE id = ?`, summary, stamp(s.now()), rid); err != nil {
		return "", fmt.Errorf("finish the reflection: %w", err)
	}
	s.events.Invalidate(DomainJournal)
	return summary, nil
}

// grounded checks the turns a change cites: each must be in the transcript
// she read. With need, at least one must be cited. It returns them joined, for
// the memory's source_turn_id.
func grounded(in *input, cited []string, need bool) (string, bool) {
	var ids []string
	for _, c := range cited {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !in.turns[c] {
			return "", false
		}
		ids = append(ids, c)
	}
	if need && len(ids) == 0 {
		return "", false
	}
	return strings.Join(ids, ","), true
}

// errForgotten stops a reflection planned before a memory was forgotten.
var errForgotten = errors.New("a memory was forgotten after the plan was made")

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
