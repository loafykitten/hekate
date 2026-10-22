package mail

import (
	"fmt"
	"strings"
	"testing"
)

func (f *fixture) addRule(kind, scope string) {
	f.t.Helper()
	if _, err := f.store.DB().Exec(`INSERT INTO approval_rules (kind, scope, scope_label, created_at) VALUES (?, ?, ?, ?)`,
		kind, scope, "mail from "+scope, "2026-01-01T00:00:00Z"); err != nil {
		f.t.Fatal(err)
	}
}

func TestSearchFoldersPaginationAndOr(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Receipt one", from: "Eleven Labs <billing@elevenlabs.example>"})
	f.deliver("INBOX", mailSpec{subject: "Promo two", from: "ElevenLabs <news@elevenlabs.example>"})
	f.deliver("INBOX", mailSpec{subject: "Archived three", from: "Shop <hi@shop.example>"})
	f.deliver("INBOX", mailSpec{subject: "Trashed four", from: "Shop <hi@shop.example>"})
	f.sweep()
	f.mustCall("mail_archive", `{"ids":`+ids(f, "Archived three")+`}`)
	f.mustCall("mail_trash", `{"ids":`+ids(f, "Trashed four")+`}`)
	if _, err := f.appr.run(t.Context(), 0); err != nil {
		t.Fatal(err)
	}

	res := f.mustCall("mail_search", `{"from":"@shop.example"}`)
	if !strings.Contains(res.Content, "No matching mail in the inbox.") || !strings.Contains(res.Content, "1 archive, 1 trash") {
		t.Errorf("default is the inbox only, and says where else it matches: %q", res.Content)
	}
	res = f.mustCall("mail_search", `{"from":"@shop.example","folder":"archive"}`)
	if !strings.Contains(res.Content, "Archived three") || strings.Contains(res.Content, "Trashed four") {
		t.Errorf("archive: %q", res.Content)
	}
	res = f.mustCall("mail_search", `{"from":"@shop.example","folder":"trash"}`)
	if !strings.Contains(res.Content, "Trashed four") || strings.Contains(res.Content, "Archived three") {
		t.Errorf("trash: %q", res.Content)
	}
	res = f.mustCall("mail_search", `{"folder":"all"}`)
	if !strings.Contains(res.Content, "Matches by folder: 2 inbox, 1 archive, 1 trash.") {
		t.Errorf("all: %q", res.Content)
	}
	if _, err := f.call("mail_search", `{"folder":"spam"}`); err == nil {
		t.Error("unknown folder accepted")
	}

	// OR means any of.
	res = f.mustCall("mail_search", `{"query":"eleven labs OR elevenlabs OR eleven"}`)
	if !strings.Contains(res.Content, "Receipt one") || !strings.Contains(res.Content, "Promo two") {
		t.Errorf("OR: %q", res.Content)
	}
	res = f.mustCall("mail_search", `{"query":"receipt OR archived","folder":"all"}`)
	if !strings.Contains(res.Content, "Receipt one") || !strings.Contains(res.Content, "Archived three") || strings.Contains(res.Content, "Promo two") {
		t.Errorf("OR across folders: %q", res.Content)
	}
	if got := ftsQuery("a OR OR b c"); got != `"a"* OR "b" "c"*` {
		t.Errorf("fts = %q", got)
	}

	// More matches than the limit are said so, and offset pages on.
	res = f.mustCall("mail_search", `{"folder":"all","limit":1}`)
	if !strings.Contains(res.Content, "Showing 1-1 of 4 matches") || !strings.Contains(res.Content, "3 more: call again with offset=1") {
		t.Errorf("limit note: %q", res.Content)
	}
	res = f.mustCall("mail_search", `{"folder":"all","limit":3,"offset":3}`)
	if strings.Count(res.Content, "\nid ") != 1 || !strings.Contains(res.Content, "the last ones") {
		t.Errorf("offset: %q", res.Content)
	}
}

func TestSearchStoredFormKeepsEveryID(t *testing.T) {
	f := newFixture(t)
	var subjects []string
	for i := range 30 {
		s := fmt.Sprintf("Offer number %d with a fairly long subject line to take space", i)
		subjects = append(subjects, s)
		f.deliver("INBOX", mailSpec{subject: s, from: "Deals Daily <deals@shop.example>", body: "BODYTEXT"})
	}
	f.sweep()
	res := f.mustCall("mail_search", `{"limit":10}`)
	if res.Stored == "" || strings.Contains(res.Stored, "BODYTEXT") {
		t.Fatalf("stored = %q", res.Stored)
	}
	for _, s := range subjects[20:] { // the newest ten
		m := f.bySubject(s)
		if !strings.Contains(res.Stored, "id "+m.ID+" · Deals Daily · ") {
			t.Errorf("stored form lost id %s:\n%s", m.ID, res.Stored)
		}
	}
	if len([]rune(res.Stored)) > 1500 {
		t.Errorf("stored form is %d chars; replay clips at 1500", len([]rune(res.Stored)))
	}
}

func TestTrashNamesEveryMessageAndSkipsTrashed(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Promo A", from: "Alpha <a@alpha.example>"})
	f.deliver("INBOX", mailSpec{subject: "Promo B", from: "Beta <b@beta.example>"})
	f.deliver("INBOX", mailSpec{subject: "Old", from: "Gamma <g@gamma.example>"})
	f.sweep()
	f.mustCall("mail_trash", `{"ids":`+ids(f, "Old")+`}`)
	if _, err := f.appr.run(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	a, b, old := f.bySubject("Promo A"), f.bySubject("Promo B"), f.bySubject("Old")

	res := f.mustCall("mail_trash", `{"ids":["`+a.ID+`","`+b.ID+`","`+old.ID+`"]}`)
	if !askedApproval(res) || !strings.Contains(res.Content, "approve trashing these 2 (approval #") {
		t.Fatalf("content = %q", res.Content)
	}
	for _, want := range []string{"id " + a.ID + " · Alpha · Promo A · inbox", "id " + b.ID + " · Beta · Promo B · inbox",
		"1 message was already in Trash, left alone", "id " + old.ID + " · Gamma · Old · Trash"} {
		if !strings.Contains(res.Content, want) || !strings.Contains(res.Stored, want) {
			t.Errorf("missing %q\ncontent:\n%s\nstored:\n%s", want, res.Content, res.Stored)
		}
	}
	reqs := f.appr.requests()
	if last := reqs[len(reqs)-1]; strings.Contains(string(last.Payload), old.ID) || !strings.Contains(last.Title, "2 messages") {
		t.Errorf("the trashed message is in the approval: %+v", last)
	}
	if strings.Contains(res.Stored, "Do not do it another way") {
		t.Error("history keeps only the short headline")
	}

	// Everything already there: nothing is asked.
	n := len(f.appr.requests())
	res = f.mustCall("mail_trash", `{"ids":["`+old.ID+`"]}`)
	if len(f.appr.requests()) != n || !strings.Contains(res.Content, "Nothing to do: 1 message is already in Trash.") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestTrashSplitsStandingRuleFromApproval(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Covered 1", from: "Alpha <a@alpha.example>"})
	f.deliver("INBOX", mailSpec{subject: "Covered 2", from: "Alpha <a@alpha.example>"})
	f.deliver("INBOX", mailSpec{subject: "Other", from: "Beta <b@beta.example>"})
	f.sweep()
	f.addRule(KindTrash, "from:a@alpha.example")

	res := f.mustCall("mail_trash", `{"ids":`+ids(f, "Covered 1", "Covered 2", "Other")+`}`)
	if f.bySubject("Covered 1").Folder != "Trash" || f.bySubject("Covered 2").Folder != "Trash" {
		t.Error("covered mail did not run at once")
	}
	if f.bySubject("Other").Folder != "INBOX" {
		t.Error("uncovered mail moved without approval")
	}
	if !strings.Contains(res.Content, "Done (standing rule): Moved 2 messages to Trash") || !strings.Contains(res.Content, "approve trashing these 1 (approval #") {
		t.Errorf("content:\n%s", res.Content)
	}
	if res.Card == nil || res.Card.Kind != "approval" {
		t.Errorf("card = %+v", res.Card)
	}
	reqs := f.appr.requests()
	if len(reqs) != 2 || strings.Contains(string(reqs[1].Payload), f.bySubject("Covered 1").ID) {
		t.Errorf("requests = %+v", reqs)
	}
	for _, s := range []string{"Covered 1", "Covered 2", "Other"} {
		if !strings.Contains(res.Stored, "id "+f.bySubject(s).ID+" · ") {
			t.Errorf("stored lost %s:\n%s", s, res.Stored)
		}
	}
}

// A batch split across many covered senders still fits history, with the
// pending approval first.
func TestTrashSplitStoredFitsHistory(t *testing.T) {
	f := newFixture(t)
	var subjects []string
	for i := range 8 {
		from := fmt.Sprintf("Sender %d <news@s%d.example>", i, i)
		f.addRule(KindTrash, fmt.Sprintf("from:news@s%d.example", i))
		for j := range 3 {
			subj := fmt.Sprintf("Weekly offer %d-%d with a long enough subject", i, j)
			subjects = append(subjects, subj)
			f.deliver("INBOX", mailSpec{subject: subj, from: from})
		}
	}
	f.deliver("INBOX", mailSpec{subject: "Needs asking", from: "Other <o@other.example>"})
	f.sweep()
	res := f.mustCall("mail_trash", `{"ids":`+ids(f, append(subjects, "Needs asking")...)+`}`)
	if n := len([]rune(res.Stored)); n > 1500 {
		t.Errorf("stored form is %d chars; replay clips at 1500:\n%s", n, res.Stored)
	}
	if !strings.HasPrefix(res.Stored, "Asked ") || !strings.Contains(res.Stored, "id "+f.bySubject("Needs asking").ID+" · ") {
		t.Errorf("the pending approval and its message must come first:\n%s", res.Stored)
	}
	if strings.Count(res.Stored, "Done (standing rule)") != 8 {
		t.Errorf("every covered sender keeps its headline:\n%s", res.Stored)
	}
}

func TestArchiveListsMessagesAndSkipsArchived(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Keep", from: "Alpha <a@alpha.example>"})
	f.deliver("INBOX", mailSpec{subject: "Done", from: "Beta <b@beta.example>"})
	f.sweep()
	f.mustCall("mail_archive", `{"ids":`+ids(f, "Done")+`}`)

	res := f.mustCall("mail_archive", `{"ids":`+ids(f, "Keep", "Done")+`}`)
	k, d := f.bySubject("Keep"), f.bySubject("Done")
	if !strings.Contains(res.Content, "Archived 1 message.") || !strings.Contains(res.Content, "id "+k.ID+" · Alpha · Keep · Archive") ||
		!strings.Contains(res.Content, "already in Archive, left alone") || !strings.Contains(res.Content, "id "+d.ID+" · Beta · Done") {
		t.Errorf("content:\n%s", res.Content)
	}
	if !strings.Contains(res.Stored, "id "+k.ID+" · ") || !strings.Contains(res.Stored, "id "+d.ID+" · ") {
		t.Errorf("stored:\n%s", res.Stored)
	}
	res = f.mustCall("mail_archive", `{"ids":`+ids(f, "Done")+`}`)
	if !strings.Contains(res.Content, "Nothing to do") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestBulkArchiveApprovalListsMessages(t *testing.T) {
	f := newFixture(t)
	var subs []string
	for i := range 11 {
		s := fmt.Sprintf("Bulk %d", i)
		subs = append(subs, s)
		f.deliver("INBOX", mailSpec{subject: s, from: "Alpha <a@alpha.example>"})
	}
	f.sweep()
	res := f.mustCall("mail_archive", `{"ids":`+ids(f, subs...)+`}`)
	if !strings.Contains(res.Content, "approve archiving these 11 (approval #") {
		t.Errorf("content = %q", res.Content)
	}
	for _, s := range subs {
		if !strings.Contains(res.Content, "id "+f.bySubject(s).ID+" · Alpha · "+s) {
			t.Errorf("missing %s", s)
		}
	}
}

func TestRuleProposeStatesWhichCategoriesStillMatch(t *testing.T) {
	f := newFixture(t)
	res := f.mustCall("mail_rule_propose", `{"from":"@bank.example","action":"trash","reason":"Noise.",
		"except":[{"category":"notification"},{"category":"receipt"}]}`)
	req := f.appr.requests()[0]
	for _, text := range []string{res.Content, req.Detail} {
		if !strings.Contains(text, "will still trash mail from bank.example classified as: needs reply, important, personal, newsletter, promo, mail not sorted yet") ||
			!strings.Contains(text, "fraud alerts") || !strings.Contains(text, "match a category like promo or newsletter") {
			t.Errorf("missing category note:\n%s", text)
		}
	}

	f.mustCall("mail_rule_propose", `{"from":"@bank.example","category":"promo","action":"trash","reason":"Promos."}`)
	if d := f.appr.requests()[1].Detail; strings.Contains(d, "Heads-up") {
		t.Errorf("a category rule needs no note: %s", d)
	}
	f.mustCall("mail_rule_propose", `{"from":"@bank.example","action":"mark_read","reason":"Quiet."}`)
	if d := f.appr.requests()[2].Detail; strings.Contains(d, "Heads-up") {
		t.Errorf("mark_read needs no note: %s", d)
	}
}
