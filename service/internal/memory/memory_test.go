package memory

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

type recEvents struct {
	mu   sync.Mutex
	seen [][]string
}

func (r *recEvents) Invalidate(d ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, d)
}
func (r *recEvents) Notify(proto.Notify) {}

func (r *recEvents) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

type fixture struct {
	svc    *Service
	events *recEvents
	loc    *time.Location
	now    time.Time
}

// newFixture starts on Monday 2026-10-19 14:00 in America/Chicago.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{events: &recEvents{}, loc: loc, now: time.Date(2026, 10, 19, 14, 0, 0, 0, loc)}
	f.svc, err = New(Options{Store: db, Location: loc, Events: f.events, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) save(t *testing.T, kind, content string, importance int) Memory {
	t.Helper()
	m, _, err := f.svc.Save(t.Context(), NewMemory{Kind: kind, Content: content, Importance: importance})
	if err != nil {
		t.Fatalf("Save(%q): %v", content, err)
	}
	return m
}

func ids(ms []Memory) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func contents(ms []Memory) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Content
	}
	return out
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status {
		t.Fatalf("err = %v, want status %d", err, status)
	}
}

func ptr[T any](v T) *T { return &v }

func TestMatchQuery(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain words", "Rika is vegetarian", `"rika" OR "vegetarian"`},
		{"stopwords go", "what should I cook tonight?", `"cook"`},
		{"nothing left", "what is it?", ``},
		{"empty", "", ``},
		{"punctuation only", `"(*)" -- :: ^ ~`, ``},
		{"quotes cannot escape", `she said "hello" OR "x`, `"said"`},
		{"operators are plain words", `cats AND dogs NOT birds NEAR(a b) OR fish`, `"cats" OR "dogs" OR "birds" OR "near" OR "fish"`},
		{"column filter", `content:secret kind: fact`, `"content" OR "secret" OR "kind" OR "fact"`},
		{"prefix star and minus", `veg* -meat +fish`, `"veg" OR "meat" OR "fish"`},
		{"parentheses", `((cats) (dogs))`, `"cats" OR "dogs"`},
		{"sql-ish", `'; DROP TABLE memories; --`, `"drop" OR "table" OR "memories"`},
		{"unicode letters", `Café Müller, naïve Zoë`, `"café" OR "müller" OR "naïve" OR "zoë"`},
		{"cjk", `私は猫が好き`, `"私は猫が好き"`},
		{"digits", `room 42b on 2026-10-21`, `"room" OR "42b" OR "2026" OR "10" OR "21"`},
		{"emoji dropped", `🍣 sushi 🍣`, `"sushi"`},
		{"contractions", `don't I like it`, `"like"`},
		{"duplicates", `tea Tea TEA coffee`, `"tea" OR "coffee"`},
		{"single letters", `a b c vitamin d`, `"vitamin"`},
		{"cap", `one two three four five six seven eight nine ten eleven twelve thirteen fourteen`, `"one" OR "two" OR "three" OR "four" OR "five" OR "six" OR "seven" OR "eight" OR "nine" OR "ten" OR "eleven" OR "twelve"`},
		{"decomposed accents", "cafe\u0301", "\"cafe\u0301\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchQuery(tt.in); got != tt.want {
				t.Errorf("MatchQuery(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// Whatever the user types must reach SQLite as a valid expression.
func TestSearchNeverFailsOnHostileText(t *testing.T) {
	f := newFixture(t)
	f.save(t, "fact", "Likes the cafe on Main Street", 3)
	for _, q := range []string{
		`"`, `""`, `"unterminated`, `)(`, `AND`, `OR OR OR`, `NOT`, `NEAR(`, `a:b`, `*`, `-`, `^x`, `{x}`, `\`,
		"x\x00y", strings.Repeat("word ", 5000), `content MATCH 'x'`, `' OR 1=1 --`, "🙂", "\u202e", `cafe"` + ` OR "street`,
	} {
		if _, err := f.svc.Search(t.Context(), q, "", 10); err != nil {
			t.Errorf("Search(%q): %v", q, err)
		}
	}
}

func TestSearchFindsStemsAndAccents(t *testing.T) {
	f := newFixture(t)
	f.save(t, "preference", "Loves cooking vegetarian curries", 3)
	f.save(t, "fact", "Lives near the Café Central", 3)
	f.save(t, "person", "猫の名前はミケ", 3)
	f.save(t, "fact", "Works at a bakery", 3)

	tests := map[string]string{
		"cook":       "Loves cooking vegetarian curries", // porter stemming
		"vegetarian": "Loves cooking vegetarian curries",
		"cafe":       "Lives near the Café Central", // diacritics folded both ways
		"CAFÉ":       "Lives near the Café Central",
		// No word segmentation: unspaced CJK is one token, so only a whole run matches.
		"猫の名前はミケ":  "猫の名前はミケ",
		"bakeries": "Works at a bakery",
	}
	for q, want := range tests {
		got, err := f.svc.Search(t.Context(), q, "", 5)
		if err != nil || len(got) != 1 || got[0].Content != want {
			t.Errorf("Search(%q) = %q (%v), want [%q]", q, contents(got), err, want)
		}
	}
	if got, _ := f.svc.Search(t.Context(), "the and of", "", 5); len(got) != 0 {
		t.Errorf("stopword-only search found %q", contents(got))
	}
	if got, _ := f.svc.Search(t.Context(), "cook", "fact", 5); len(got) != 0 {
		t.Errorf("kind filter ignored: %q", contents(got))
	}
	if _, err := f.svc.Search(t.Context(), "cook", "bogus", 5); err == nil {
		t.Error("bad kind accepted")
	}
}

func TestRecallRanking(t *testing.T) {
	f := newFixture(t)
	// Same words, different importance: the important one first.
	low := f.save(t, "fact", "Likes green tea in the evening", 1)
	high := f.save(t, "fact", "Likes green tea in the morning", 5)
	mid := f.save(t, "fact", "Likes green tea in the afternoon", 3)
	// A memory that only shares one word ranks below ones matching more.
	partial := f.save(t, "fact", "Tea shop opens at nine", 3)
	f.save(t, "fact", "Dentist on Friday", 5)

	got, err := f.svc.Search(t.Context(), "green tea evening", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].ID != low.ID {
		// "evening" only appears in the low-importance note, which beats the importance boost.
		t.Fatalf("ranked %q, want the note matching all three words first", contents(got))
	}
	got, _ = f.svc.Search(t.Context(), "green tea", "", 10)
	want := []string{high.ID, mid.ID, low.ID, partial.ID}
	if !slices.Equal(ids(got), want) {
		t.Errorf("by importance: got %q, want %q", contents(got), want)
	}

	// The limit applies after ranking.
	if got, _ := f.svc.Search(t.Context(), "green tea", "", 1); len(got) != 1 || got[0].ID != high.ID {
		t.Errorf("limit 1 = %q", contents(got))
	}
}

func TestIndexFollowsEdits(t *testing.T) {
	f := newFixture(t)
	m := f.save(t, "fact", "Drives a blue Corolla", 3)
	other := f.save(t, "fact", "Owns a Corolla manual", 3)

	if got, _ := f.svc.Search(t.Context(), "corolla", "", 10); len(got) != 2 {
		t.Fatalf("before edit: %q", contents(got))
	}
	if _, err := f.svc.Update(t.Context(), mustID(m.ID), Patch{Content: ptr("Drives a red Civic")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Search(t.Context(), "corolla", "", 10); len(got) != 1 || got[0].ID != other.ID {
		t.Errorf("after edit, corolla = %q", contents(got))
	}
	if got, _ := f.svc.Search(t.Context(), "civic", "", 10); len(got) != 1 || got[0].ID != m.ID {
		t.Errorf("after edit, civic = %q", contents(got))
	}
	// Changing only the importance keeps the index intact.
	if _, err := f.svc.Update(t.Context(), mustID(m.ID), Patch{Importance: ptr(5)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Search(t.Context(), "civic", "", 10); len(got) != 1 {
		t.Errorf("after importance change, civic = %q", contents(got))
	}

	if _, err := f.svc.Forget(t.Context(), mustID(m.ID)); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Search(t.Context(), "civic", "", 10); len(got) != 0 {
		t.Errorf("forgotten memory still found: %q", contents(got))
	}
	// Hard delete: nothing is left in the table or the index.
	var n int
	if err := f.svc.db.QueryRow(`SELECT count(*) FROM memories WHERE id = ?`, mustID(m.ID)).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left = %d, %v", n, err)
	}
	if err := f.svc.db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH '"civic"'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("index entries left = %d, %v", n, err)
	}
	// The index stays consistent with the table.
	if _, err := f.svc.db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("fts integrity check: %v", err)
	}
	_, err := f.svc.Forget(t.Context(), mustID(m.ID))
	wantStatus(t, err, http.StatusNotFound)
}

func mustID(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestSaveValidationAndDuplicates(t *testing.T) {
	f := newFixture(t)
	for name, n := range map[string]NewMemory{
		"bad kind":       {Kind: "gossip", Content: "x"},
		"no kind":        {Content: "x"},
		"no content":     {Kind: "fact", Content: "  "},
		"long content":   {Kind: "fact", Content: strings.Repeat("x", maxContent+1)},
		"importance 6":   {Kind: "fact", Content: "x", Importance: 6},
		"importance -1":  {Kind: "fact", Content: "x", Importance: -1},
		"bad follow-up":  {Kind: "fact", Content: "x", FollowUpAt: "soon"},
		"bad follow-up2": {Kind: "fact", Content: "x", FollowUpAt: "2026-13-01"},
	} {
		_, _, err := f.svc.Save(t.Context(), n)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		wantStatus(t, err, http.StatusBadRequest)
	}
	if f.events.count() != 0 {
		t.Errorf("failed saves emitted %d events", f.events.count())
	}

	m, existed, err := f.svc.Save(t.Context(), NewMemory{Kind: "preference", Content: " Takes coffee black ", SourceTurn: "t_1"})
	if err != nil || existed || m.Importance != DefaultImportance || m.Content != "Takes coffee black" || m.FollowUpAt != nil {
		t.Fatalf("save: %v %+v existed=%v", err, m, existed)
	}
	var src string
	if err := f.svc.db.QueryRow(`SELECT source_turn_id FROM memories`).Scan(&src); err != nil || src != "t_1" {
		t.Errorf("source turn = %q, %v", src, err)
	}
	again, existed, err := f.svc.Save(t.Context(), NewMemory{Kind: "preference", Content: "takes COFFEE black", Importance: 4})
	if err != nil || !existed || again.ID != m.ID || again.Importance != 4 {
		t.Errorf("duplicate: %v %+v existed=%v", err, again, existed)
	}
	if all, _ := f.svc.List(t.Context(), "", 0); len(all) != 1 {
		t.Errorf("duplicates stored: %q", contents(all))
	}
}

func TestFollowUpTimes(t *testing.T) {
	f := newFixture(t)
	date, _, err := f.svc.Save(t.Context(), NewMemory{Kind: "follow_up", Content: "Interview at Acme", FollowUpAt: "2026-10-22"})
	if err != nil || date.FollowUpAt == nil || *date.FollowUpAt != "2026-10-22T09:00:00-05:00" {
		t.Fatalf("date-only follow-up: %v %+v", err, date)
	}
	timed, _, _ := f.svc.Save(t.Context(), NewMemory{Kind: "follow_up", Content: "Ask about the move", FollowUpAt: "2026-10-20T15:30"})
	if *timed.FollowUpAt != "2026-10-20T15:30:00-05:00" {
		t.Errorf("timed follow-up = %s", *timed.FollowUpAt)
	}
	up, err := f.svc.Update(t.Context(), mustID(timed.ID), Patch{FollowUpAt: ptr("")})
	if err != nil || up.FollowUpAt != nil {
		t.Errorf("clear follow-up: %v %+v", err, up)
	}
	due, err := f.svc.FollowUpsDue(t.Context(), f.now.Add(72*time.Hour), 10)
	if err != nil || len(due) != 1 || due[0].ID != date.ID {
		t.Errorf("due = %q, %v", contents(due), err)
	}
}

func TestUpdateValidation(t *testing.T) {
	f := newFixture(t)
	m := f.save(t, "fact", "Original", 3)
	id := mustID(m.ID)
	for name, p := range map[string]Patch{
		"empty content": {Content: ptr(" ")}, "bad kind": {Kind: ptr("rumor")}, "importance 0": {Importance: ptr(0)},
		"bad follow-up": {FollowUpAt: ptr("whenever")},
	} {
		if _, err := f.svc.Update(t.Context(), id, p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	_, err := f.svc.Update(t.Context(), 99, Patch{Content: ptr("x")})
	wantStatus(t, err, http.StatusNotFound)
	if got, _ := f.svc.Get(t.Context(), id); got.Content != "Original" {
		t.Errorf("failed updates changed it: %+v", got)
	}
}

func TestProfile(t *testing.T) {
	f := newFixture(t)
	e, err := f.svc.SetProfile(t.Context(), "  Favorite  Color ", " green ")
	if err != nil || e.Key != "favorite_color" || e.Value != "green" || e.UpdatedAt != "2026-10-19T14:00:00-05:00" {
		t.Fatalf("set: %v %+v", err, e)
	}
	if _, err := f.svc.SetProfile(t.Context(), "favorite_color", "blue"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetProfile(t.Context(), "diet", "vegetarian"); err != nil {
		t.Fatal(err)
	}
	p, _ := f.svc.Profile(t.Context())
	if len(p) != 2 || p[0].Key != "diet" || p[1].Value != "blue" {
		t.Errorf("profile = %+v", p)
	}
	for name, kv := range map[string][2]string{
		"no key": {" ", "x"}, "no value": {"k", " "}, "long key": {strings.Repeat("k", maxKey+1), "x"},
		"long value": {"k", strings.Repeat("v", maxValue+1)},
	} {
		if _, err := f.svc.SetProfile(t.Context(), kv[0], kv[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := f.svc.RemoveProfile(t.Context(), "Diet"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.svc.RemoveProfile(t.Context(), "diet"), http.StatusNotFound)

	for i := range maxProfileKeys {
		if _, err := f.svc.SetProfile(t.Context(), "key"+string(rune('a'+i%26))+string(rune('a'+i/26)), "v"); err != nil && i < maxProfileKeys-1 {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	_, err = f.svc.SetProfile(t.Context(), "one too many", "v")
	wantStatus(t, err, http.StatusConflict)
	if _, err := f.svc.SetProfile(t.Context(), "favorite_color", "red"); err != nil {
		t.Errorf("replacing an entry in a full profile failed: %v", err)
	}
}

func TestPromptContext(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if got, err := f.svc.PromptContext(ctx, f.now); err != nil || got != "" {
		t.Fatalf("empty store: %q %v", got, err)
	}

	f.svc.SetProfile(ctx, "diet", "vegetarian")    //nolint:errcheck // fixture setup
	f.svc.SetProfile(ctx, "pronouns", "they/them") //nolint:errcheck // fixture setup
	soon, _, _ := f.svc.Save(ctx, NewMemory{Kind: "follow_up", Content: "Job interview at Acme", FollowUpAt: "2026-10-20T08:00"})
	f.svc.Save(ctx, NewMemory{Kind: "follow_up", Content: "Ask about the trip to Lisbon", FollowUpAt: "2026-11-30"}) //nolint:errcheck // fixture setup
	pinned := f.save(t, "fact", "Allergic to penicillin", 5)
	related := f.save(t, "preference", "Enjoys Lisbon food markets", 3)
	f.save(t, "fact", "Cat is called Miso", 3)
	// Matches the message, but is already shown as a follow-up, so not repeated.
	f.save(t, "fact", "Interview prep notes live in the blue folder", 3)

	got, err := f.svc.PromptContextFor(ctx, f.now, "Any tips for my interview? Also Lisbon!")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Profile:\n- diet: vegetarian\n- pronouns: they/them",
		"Worth asking about now",
		"- [" + soon.ID + "] (follow_up) Job interview at Acme (ask on 2026-10-20T08:00:00-05:00)",
		"Never forget:\n- [" + pinned.ID + "] (fact) Allergic to penicillin",
		"- [" + related.ID + "] (preference) Enjoys Lisbon food markets",
		"Interview prep notes live in the blue folder",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Cat is called Miso", "trip to Lisbon (ask on"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("context has %q:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "Job interview at Acme") != 1 {
		t.Errorf("follow-up listed twice:\n%s", got)
	}

	// Without a message only the always-on parts remain.
	got, _ = f.svc.PromptContext(ctx, f.now)
	if strings.Contains(got, "Lisbon food") || !strings.Contains(got, "Profile:") || !strings.Contains(got, "Never forget") {
		t.Errorf("no-message context:\n%s", got)
	}

	// Follow-ups beyond a day away stay out until they get close.
	if strings.Contains(got, "trip to Lisbon") {
		t.Errorf("a far follow-up leaked:\n%s", got)
	}
	got, _ = f.svc.PromptContext(ctx, time.Date(2026, 11, 29, 12, 0, 0, 0, f.loc))
	if !strings.Contains(got, "trip to Lisbon") {
		t.Errorf("a due follow-up is missing:\n%s", got)
	}
}

// Two patches touching different fields at the same time must both survive.
func TestConcurrentPatchesKeepEachOthersFields(t *testing.T) {
	f := newFixture(t)
	for i := range 20 {
		m, _, err := f.svc.Save(t.Context(), NewMemory{Kind: "follow_up", Content: fmt.Sprintf("Interview %d", i), FollowUpAt: "2026-10-22"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := strconv.ParseInt(m.ID, 10, 64)
		content, clear := fmt.Sprintf("Interview %d at Acme", i), ""
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = f.svc.Update(t.Context(), id, Patch{Content: &content}) }()
		go func() { defer wg.Done(); _, _ = f.svc.Update(t.Context(), id, Patch{FollowUpAt: &clear}) }()
		wg.Wait()
		got, err := f.svc.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != content || got.FollowUpAt != nil {
			t.Fatalf("round %d: content=%q follow_up=%v; a patch lost the other's change", i, got.Content, got.FollowUpAt)
		}
	}
}
