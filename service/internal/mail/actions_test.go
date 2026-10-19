package mail

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestArchiveUpdatesCacheThroughCOPYUID(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Keep"})
	f.deliver("INBOX", mailSpec{subject: "File me", body: "Body to read after the move."})
	f.sweep()
	id := f.id("File me")

	out, err := f.svc.Archive(t.Context(), []int64{id})
	if err != nil || out.Done != 1 || out.Dest != "Archive" {
		t.Fatalf("archive = %+v %v", out, err)
	}
	if got := f.serverUIDs("INBOX"); len(got) != 1 {
		t.Errorf("inbox UIDs = %v, want 1", got)
	}
	if got := f.serverUIDs("Archive"); len(got) != 1 {
		t.Errorf("archive UIDs = %v, want 1", got)
	}
	m, err := f.svc.Message(t.Context(), id)
	if err != nil || m.Folder != "Archive" || m.Subject != "File me" {
		t.Fatalf("message after archive = %+v %v (the id must survive a move)", m, err)
	}
	// Still addressable by its Hekate id: the cache holds the new UID.
	_, body, err := f.svc.ReadBody(t.Context(), id)
	if err != nil || !strings.Contains(body.Text, "Body to read after the move.") {
		t.Errorf("read after archive = %+v %v", body, err)
	}
	// A later sweep must not treat it as gone, and the inbox no longer lists it.
	f.sweep()
	if m, _ := f.svc.Message(t.Context(), id); m.Folder != "Archive" {
		t.Errorf("after sweep = %+v", m)
	}
	if got := f.inbox(); len(got) != 1 || got[0].Subject != "Keep" {
		t.Errorf("inbox = %+v", got)
	}
	// Archived mail is still searchable across folders, and can be moved on.
	all, _ := f.svc.Messages(t.Context(), Query{AllFolders: true, Text: "file"})
	if len(all) != 1 || all[0].Folder != "Archive" {
		t.Errorf("search all = %+v", all)
	}
	if out, err := f.svc.MoveTo(t.Context(), []int64{id}, "projects"); err != nil || out.Dest != "Projects" {
		t.Errorf("move on = %+v %v", out, err)
	}
	if got := f.serverUIDs("Projects"); len(got) != 1 {
		t.Errorf("projects UIDs = %v", got)
	}
}

func TestTrashFindsTrashFolder(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Junk one"})
	f.deliver("INBOX", mailSpec{subject: "Junk two"})
	f.sweep()
	out, err := f.svc.Trash(t.Context(), []int64{f.id("Junk one"), f.id("Junk two")})
	if err != nil || out.Done != 2 || out.Dest != "Trash" {
		t.Fatalf("trash = %+v %v", out, err)
	}
	if len(f.serverUIDs("Trash")) != 2 || len(f.serverUIDs("INBOX")) != 0 {
		t.Errorf("trash=%v inbox=%v", f.serverUIDs("Trash"), f.serverUIDs("INBOX"))
	}
	if m := f.bySubject("Junk one"); m.Folder != "Trash" {
		t.Errorf("cached = %+v", m)
	}
}

func TestMoveToValidatesFolder(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Somewhere"})
	f.sweep()
	id := f.id("Somewhere")
	_, err := f.svc.MoveTo(t.Context(), []int64{id}, "Nope")
	if err == nil || !strings.Contains(err.Error(), `no folder named "Nope"`) || !strings.Contains(err.Error(), "Projects") {
		t.Errorf("missing folder err = %v (want the folder list)", err)
	}
	// Trashing must not slip through the move tool.
	_, err = f.svc.MoveTo(t.Context(), []int64{id}, "trash")
	if err == nil || !strings.Contains(err.Error(), "mail_trash") {
		t.Errorf("move to trash err = %v", err)
	}
	if len(f.serverUIDs("INBOX")) != 1 {
		t.Error("the message moved despite the refusals")
	}
	if _, err := f.svc.MoveTo(t.Context(), []int64{id}, " "); err == nil {
		t.Error("blank folder accepted")
	}
}

func TestArchiveCreatesMissingArchiveFolder(t *testing.T) {
	f := newFixture(t)
	f.svc.accounts[0].cfg.Archive = "Filed"
	f.deliver("INBOX", mailSpec{subject: "A"})
	f.sweep()
	if out, err := f.svc.Archive(t.Context(), []int64{f.id("A")}); err != nil || out.Dest != "Filed" {
		t.Fatalf("archive = %+v %v", out, err)
	}
	if len(f.serverUIDs("Filed")) != 1 {
		t.Error("message not in the new folder")
	}
}

func TestMarkReadWritesFlagsAndCache(t *testing.T) {
	f := newFixture(t)
	uid := f.deliver("INBOX", mailSpec{subject: "Flag me"})
	f.sweep()
	id := f.id("Flag me")
	if out, err := f.svc.MarkRead(t.Context(), []int64{id}); err != nil || out.Done != 1 {
		t.Fatalf("mark read = %+v %v", out, err)
	}
	if !slices.Contains(f.serverFlags("INBOX", uid), imap.FlagSeen) || f.bySubject("Flag me").Unread {
		t.Error("read state not applied on the server and in the cache")
	}
	if _, err := f.svc.MarkUnread(t.Context(), []int64{id}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.serverFlags("INBOX", uid), imap.FlagSeen) || !f.bySubject("Flag me").Unread {
		t.Error("unread state not applied")
	}
}

func TestReadingDoesNotMarkRead(t *testing.T) {
	f := newFixture(t)
	uid := f.deliver("INBOX", mailSpec{subject: "Long", body: strings.Repeat("word ", 4000)})
	f.sweep()
	_, body, err := f.svc.ReadBody(t.Context(), f.id("Long"))
	if err != nil {
		t.Fatal(err)
	}
	if !body.Truncated || len([]rune(body.Text)) > maxBodyRunes+1 {
		t.Errorf("body truncated=%v len=%d, want a cut at about %d", body.Truncated, len([]rune(body.Text)), maxBodyRunes)
	}
	if slices.Contains(f.serverFlags("INBOX", uid), imap.FlagSeen) || !f.bySubject("Long").Unread {
		t.Error("reading marked the message read")
	}
}

func TestReadHTMLMessage(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Pretty", html: true, body: `<html><head><style>p{color:red}</style></head><body><h1>Hi Rika</h1><p>Your <b>order</b> shipped.</p><script>alert(1)</script></body></html>`})
	f.sweep()
	_, body, err := f.svc.ReadBody(t.Context(), f.id("Pretty"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Text, "Hi Rika") || !strings.Contains(body.Text, "Your order shipped.") ||
		strings.Contains(body.Text, "alert") || strings.Contains(body.Text, "color") || strings.Contains(body.Text, "<") {
		t.Errorf("html text = %q", body.Text)
	}
}

func TestActionsOnMessagesGoneFromServer(t *testing.T) {
	f := newFixture(t)
	uid := f.deliver("INBOX", mailSpec{subject: "Deleted elsewhere"})
	f.deliver("INBOX", mailSpec{subject: "Present"})
	f.sweep()
	f.expungeFrom("INBOX", uid)
	out, err := f.svc.Archive(t.Context(), []int64{f.id("Deleted elsewhere"), f.id("Present")})
	if err != nil || out.Done != 1 || out.Missing != 1 {
		t.Fatalf("archive = %+v %v, want 1 done and 1 missing", out, err)
	}
	if m := f.bySubject("Deleted elsewhere"); m.Folder != "other" {
		t.Errorf("deleted message = %+v", m)
	}
	if _, err := f.svc.Archive(t.Context(), []int64{424242}); err == nil || !strings.Contains(err.Error(), "424242") {
		t.Errorf("unknown id err = %v", err)
	}
}

func TestNoMoveCapabilityLeavesMailAlone(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noMove = true })
	f.deliver("INBOX", mailSpec{subject: "Stay put"})
	f.sweep()
	id := f.id("Stay put")
	if _, err := f.svc.Archive(t.Context(), []int64{id}); !errors.Is(err, errNoMove) {
		t.Fatalf("archive without MOVE = %v, want errNoMove", err)
	}
	if got := f.serverUIDs("INBOX"); len(got) != 1 {
		t.Errorf("inbox UIDs = %v, want the message still there", got)
	}
	if got := f.serverUIDs("Archive"); len(got) != 0 {
		t.Errorf("archive UIDs = %v, want none", got)
	}
}

func TestMarkReadCountsVanishedMessages(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Here"})
	gone := f.deliver("INBOX", mailSpec{subject: "Deleted elsewhere"})
	f.sweep()
	here, vanished := f.id("Here"), f.id("Deleted elsewhere")
	f.expungeFrom("INBOX", gone) // another client deletes it before the next sweep

	out, err := f.svc.MarkRead(t.Context(), []int64{here, vanished})
	if err != nil || out.Done != 1 || out.Missing != 1 {
		t.Fatalf("mark read = %+v, %v (the vanished message must not count as done)", out, err)
	}
	if m, err := f.svc.Message(t.Context(), vanished); err != nil || m.Folder != "other" {
		t.Errorf("vanished message = %+v, %v (want it marked as gone elsewhere)", m, err)
	}
}
