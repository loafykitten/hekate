package store

import (
	"context"
	"testing"
)

// 0012 rebuilds mail_rules to allow unsubscribe: rows, ids, the id sequence and
// every column carry over, and an unsubscribe rule must name a sender.
func TestMailRulesUnsubscribeMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var before []migration
	for _, m := range migrations {
		if m.version < 12 {
			before = append(before, m)
		}
	}
	s, err := open(ctx, dir, before)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	for _, q := range []string{
		`INSERT INTO mail_rules (match_from, match_subject, match_body, except_json, place, action, label, hits, created_at)
			VALUES ('@example.org', 'upgrade', 'offer', '[{"category":"receipt"}]', 1, 'archive', 'Archive mail from example.org', 4, 't')`,
		`INSERT INTO mail_rules (match_category, action, folder, label, created_at) VALUES ('promo', 'move', 'Deals', 'Move promo mail to Deals', 't')`,
		`DELETE FROM mail_rules WHERE id = 2`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mail_rules (match_from, action, label, created_at) VALUES ('@example.org', 'unsubscribe', 'x', 't')`); err == nil {
		t.Fatal("the old schema accepted unsubscribe")
	}
	_ = s.Close()

	s, err = open(ctx, dir, migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	db = s.DB()
	var id, hits, place int
	var from, subject, body, except, action string
	if err := db.QueryRowContext(ctx, `SELECT id, match_from, match_subject, match_body, except_json, place, action, hits FROM mail_rules`).
		Scan(&id, &from, &subject, &body, &except, &place, &action, &hits); err != nil {
		t.Fatal(err)
	}
	if id != 1 || from != "@example.org" || subject != "upgrade" || body != "offer" || except != `[{"category":"receipt"}]` ||
		place != 1 || action != "archive" || hits != 4 {
		t.Errorf("rule = %d %s %s %s %s %d %s %d", id, from, subject, body, except, place, action, hits)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO mail_rules (match_from, action, label, created_at) VALUES ('@example.org', 'unsubscribe', 'Unsubscribe from example.org', 't')`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.LastInsertId(); n != 3 {
		t.Errorf("new rule id = %d, want 3 (a deleted rule's id is not reused)", n)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mail_rules (match_category, action, label, created_at) VALUES ('promo', 'unsubscribe', 'x', 't')`); err == nil {
		t.Error("an unsubscribe rule without a sender was stored")
	}
}
