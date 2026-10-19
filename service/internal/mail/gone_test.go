package mail

import "testing"

func TestMarkGoneAcrossFoldersWithTheSameUID(t *testing.T) {
	f := newFixture(t)
	db := f.store.DB()
	var ids []int64
	for _, folder := range []string{"INBOX", "Archive"} {
		res, err := db.ExecContext(t.Context(), `INSERT INTO mail_messages (account, folder, uidvalidity, uid, date, first_seen_at)
			VALUES ('personal', ?, 1, 7, '2026-10-19T10:00:00Z', '2026-10-19T10:00:00Z')`, folder)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	for _, id := range ids {
		f.svc.markGone(t.Context(), id)
	}
	var gone int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM mail_messages WHERE gone = 1`).Scan(&gone); err != nil || gone != 2 {
		t.Fatalf("gone rows = %d, %v (both must be marked)", gone, err)
	}
}
