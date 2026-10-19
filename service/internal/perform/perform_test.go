package perform

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/voice"
)

var vocab = Vocabulary{Gestures: []string{"waving", "thankful", "no"}, Expressions: DefaultExpressions}

func feed(chunks ...string) []string {
	var s Splitter
	var out []string
	for _, c := range chunks {
		out = append(out, s.Push(c)...)
	}
	return append(out, s.Flush()...)
}

// chars splits text into one-rune chunks, the worst case for a streaming splitter.
func chars(text string) []string {
	var out []string
	for _, r := range text {
		out = append(out, string(r))
	}
	return out
}

func TestSplitter(t *testing.T) {
	long := strings.Repeat("word ", 30) + "this goes on, and on and on " + strings.Repeat("filler ", 30) + "to the very end."
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"two sentences", "Good morning, Ember. Did you sleep well?", []string{"Good morning, Ember.", "Did you sleep well?"}},
		{"keeps trailing fragment", "There you are. And then some", []string{"There you are.", "And then some"}},
		{"decimal", "The total comes to 3.5 hours of work today. Fine.", []string{"The total comes to 3.5 hours of work today.", "Fine."}},
		{"time", "Your meeting is at 5 p.m. tomorrow, so rest now. Okay?", []string{"Your meeting is at 5 p.m. tomorrow, so rest now.", "Okay?"}},
		{"abbreviations", "Ask Dr. Smith about it e.g. tomorrow vs. Friday etc. Then we will see.", []string{"Ask Dr. Smith about it e.g. tomorrow vs. Friday etc. Then we will see."}},
		{"mr mrs st", "Mr. and Mrs. Jones live on St. James street. Nice.", []string{"Mr. and Mrs. Jones live on St. James street.", "Nice."}},
		{"initials", "I asked J. K. Rowling about the book. Yes.", []string{"I asked J. K. Rowling about the book.", "Yes."}},
		{"sentence ending in I", "That is what we all do, and so do I. Next one comes.", []string{"That is what we all do, and so do I.", "Next one comes."}},
		{"short fragment merges forward", "Hi! Welcome back to the den, Ember.", []string{"Hi! Welcome back to the den, Ember."}},
		{"short fragments chain", "Ok. So. Then we can finally go and see.", []string{"Ok. So. Then we can finally go and see."}},
		{"flush keeps short last", "That was a long and lovely day. Yes.", []string{"That was a long and lovely day.", "Yes."}},
		{"flush joins carry and rest", "Yes. And the rest", []string{"Yes. And the rest"}},
		{"gesture tag is not spoken length", "{waving} Hi! How was your whole night, truly?", []string{"{waving} Hi! How was your whole night, truly?"}},
		{"never split inside curly", "I will stay close. {waving. not done} And continue the story.", []string{"I will stay close.", "{waving. not done} And continue the story."}},
		{"never split inside square", "Fair enough. [laughs. really] Then we go on and on.", []string{"Fair enough.", "[laughs. really] Then we go on and on."}},
		{"ellipsis", "Well... I suppose that works for us. Done?!", []string{"Well... I suppose that works for us.", "Done?!"}},
		{"unicode ellipsis", "Hmm… that is quite a thought to chew. Yes.", []string{"Hmm… that is quite a thought to chew.", "Yes."}},
		{"closing quote", `She said "go home now." Then she left the building.`, []string{`She said "go home now."`, "Then she left the building."}},
		{"newline splits", "First line is long enough\nSecond line is long enough too", []string{"First line is long enough", "Second line is long enough too"}},
		{"list numbers", "1. Buy some milk today\n2. Call the bank tomorrow", []string{"1. Buy some milk today", "2. Call the bank tomorrow"}},
		{"no space after period", "Visit example.com for details today. Thanks.", []string{"Visit example.com for details today.", "Thanks."}},
		{"unclosed brace is text", "A stray { bracket sits here and " + strings.Repeat("x", 100) + ". Then the next one.", []string{"A stray { bracket sits here and " + strings.Repeat("x", 100) + ".", "Then the next one."}},
		{"empty", "", nil},
		{"whitespace only", "   \n  ", nil},
		{"long sentence splits at soft break", long, nil}, // checked separately below
	}
	for _, tt := range tests {
		if tt.want == nil && tt.in != "" && strings.TrimSpace(tt.in) != "" {
			continue
		}
		t.Run(tt.name+"/whole", func(t *testing.T) {
			if got := feed(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
		t.Run(tt.name+"/chars", func(t *testing.T) {
			if got := feed(chars(tt.in)...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestSplitterLongSentence(t *testing.T) {
	long := strings.Repeat("word ", 30) + "this goes on, and on and on " + strings.Repeat("filler ", 30) + "to the very end."
	for name, chunks := range map[string][]string{"whole": {long}, "chars": chars(long)} {
		t.Run(name, func(t *testing.T) {
			got := feed(chunks...)
			if len(got) < 2 {
				t.Fatalf("not split: %q", got)
			}
			if !strings.HasSuffix(got[0], "and on,") && !strings.HasSuffix(got[0], "on,") {
				t.Errorf("first part should end at the comma: %q", got[0])
			}
			if strings.Join(got, " ") != long {
				t.Errorf("text changed:\n%q\n%q", strings.Join(got, " "), long)
			}
		})
	}
}

func TestSplitterStreamsEarly(t *testing.T) {
	var s Splitter
	if got := s.Push("Good morning, Ember. Did you"); !reflect.DeepEqual(got, []string{"Good morning, Ember."}) {
		t.Fatalf("got %q", got)
	}
	if got := s.Push(" sleep?"); got != nil {
		t.Fatalf("a terminator at the end waits for what follows: %q", got)
	}
	if got := s.Push(" Yes"); !reflect.DeepEqual(got, []string{"Did you sleep?"}) {
		t.Fatalf("got %q", got)
	}
	if got := s.Flush(); !reflect.DeepEqual(got, []string{"Yes"}) {
		t.Fatalf("flush = %q", got)
	}
	if got := s.Flush(); got != nil {
		t.Fatalf("second flush = %q", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		display     string
		tts         string
		gestures    []Mark
		expressions []Mark
		dropped     []string
	}{
		{"plain", "Morning, Ember.", "Morning, Ember.", "Morning, Ember.", nil, nil, nil},
		{"leading gesture", "{waving} There you are.", "There you are.", "There you are.", []Mark{{"waving", 0}}, nil, nil},
		{"mid gesture", "Hi {thankful} friend.", "Hi friend.", "Hi friend.", []Mark{{"thankful", 3}}, nil, nil},
		{"gesture case and spaces", "{ Waving } Hi.", "Hi.", "Hi.", []Mark{{"waving", 0}}, nil, nil},
		{"expression stays in tts", "[chuckles] Fair enough. Morning, Ember.", "Fair enough. Morning, Ember.", "[chuckles] Fair enough. Morning, Ember.", nil, []Mark{{"chuckles", 0}}, nil},
		{"expression mid", "Well [sighs] okay.", "Well okay.", "Well [sighs] okay.", nil, []Mark{{"sighs", 5}}, nil},
		{"both", "{no} [softly] I would not.", "I would not.", "[softly] I would not.", []Mark{{"no", 0}}, []Mark{{"softly", 0}}, nil},
		{"unknown curly dropped", "Hi {dance} there.", "Hi there.", "Hi there.", nil, nil, []string{"{dance}"}},
		{"unknown square dropped", "Hi [shouts] there.", "Hi there.", "Hi there.", nil, nil, []string{"[shouts]"}},
		{"expression not a gesture", "Hi {laughs} there.", "Hi there.", "Hi there.", nil, nil, []string{"{laughs}"}},
		{"gesture not an expression", "Hi [waving] there.", "Hi there.", "Hi there.", nil, nil, []string{"[waving]"}},
		{"markdown", "**Bold** and _soft_ and `code` # done", "Bold and soft and code done", "Bold and soft and code done", nil, nil, nil},
		{"snake case keeps a gap", "use my_var here", "use my var here", "use my var here", nil, nil, nil},
		{"list dash", "- first item", "first item", "first item", nil, nil, nil},
		{"numbered list", "2. second item", "second item", "second item", nil, nil, nil},
		{"link", "See [the docs](https://example.com/x) now.", "See the docs now.", "See the docs now.", nil, nil, nil},
		{"whitespace", "  too   many \t spaces \n here  ", "too many spaces here", "too many spaces here", nil, nil, nil},
		{"stray brackets", "odd } brackets { here ]", "odd brackets here", "odd brackets here", nil, nil, nil},
		{"unicode offsets", "Café {waving} ré.", "Café ré.", "Café ré.", []Mark{{"waving", 5}}, nil, nil},
		{"trailing gesture clamps", "Bye. {waving}", "Bye.", "Bye.", []Mark{{"waving", 4}}, nil, nil},
		{"only tags", "{waving}", "", "", []Mark{{"waving", 0}}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.in, vocab)
			if got.Display != tt.display || got.TTSText != tt.tts {
				t.Errorf("display=%q tts=%q\nwant    %q    %q", got.Display, got.TTSText, tt.display, tt.tts)
			}
			if !reflect.DeepEqual(got.Gestures, tt.gestures) {
				t.Errorf("gestures = %v, want %v", got.Gestures, tt.gestures)
			}
			if !reflect.DeepEqual(got.Expressions, tt.expressions) {
				t.Errorf("expressions = %v, want %v", got.Expressions, tt.expressions)
			}
			if !reflect.DeepEqual(got.Dropped, tt.dropped) {
				t.Errorf("dropped = %v, want %v", got.Dropped, tt.dropped)
			}
			if strings.ContainsAny(got.Display, "{}[]*_#`") {
				t.Errorf("display has residue: %q", got.Display)
			}
			// Offsets must point into the right text.
			for _, g := range got.Gestures {
				if g.Offset > len([]rune(got.Display)) {
					t.Errorf("gesture offset %d past display", g.Offset)
				}
			}
			tts := []rune(got.TTSText)
			for _, e := range got.Expressions {
				if !strings.HasPrefix(string(tts[e.Offset:]), "["+e.Name+"]") {
					t.Errorf("expression %v does not start a tag in %q", e, got.TTSText)
				}
			}
		})
	}
}

// speechFor builds an alignment where each character takes step seconds,
// except that the first prefix runes (an expression tag) share tagDur.
func speechFor(text string, tagRunes int, tagDur, step float64) *voice.Speech {
	var a voice.Alignment
	at := 0.0
	for i, r := range []rune(text) {
		d := step
		if i < tagRunes {
			d = tagDur / float64(tagRunes)
		}
		a.Chars = append(a.Chars, string(r))
		a.Starts = append(a.Starts, at)
		at += d
		a.Ends = append(a.Ends, at)
	}
	return &voice.Speech{Audio: []byte("mp3data"), Format: "mp3", Duration: at, Alignment: a}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 0.0015 }

func TestBuildRealAlignmentShape(t *testing.T) {
	// ElevenLabs eleven_v3: the [chuckles] tag is reported as characters that
	// span 0 to 0.84 s, and the spoken words follow it.
	tagged := Parse("{waving} [chuckles] Fair enough. Morning, Ember.", vocab)
	if tagged.TTSText != "[chuckles] Fair enough. Morning, Ember." {
		t.Fatalf("tts = %q", tagged.TTSText)
	}
	sp := speechFor(tagged.TTSText, len("[chuckles]"), 0.84, 0.05)
	seg := Build(3, tagged, sp)

	if seg.Seq != 3 || seg.Text != "Fair enough. Morning, Ember." {
		t.Fatalf("seg = %+v", seg)
	}
	if seg.AudioB64 == nil || *seg.AudioB64 != "bXAzZGF0YQ==" || seg.AudioFormat == nil || *seg.AudioFormat != "mp3" {
		t.Errorf("audio = %v %v", seg.AudioB64, seg.AudioFormat)
	}
	wantWords := []string{"Fair", "enough.", "Morning,", "Ember."}
	if len(seg.Words) != len(wantWords) {
		t.Fatalf("words = %+v", seg.Words)
	}
	for i, w := range seg.Words {
		if w.Text != wantWords[i] {
			t.Errorf("word %d = %q", i, w.Text)
		}
	}
	// "Fair" begins right after "[chuckles] " (0.84 s + one 0.05 s space) and lasts four characters.
	if !approx(seg.Words[0].Start, 0.89) || !approx(seg.Words[0].End, 1.09) {
		t.Errorf("Fair = %+v", seg.Words[0])
	}
	if seg.Words[0].Start < 0.84 {
		t.Error("tag characters must not leak into word timings")
	}
	if len(seg.Expressions) != 1 || seg.Expressions[0].Name != "chuckles" || seg.Expressions[0].At != 0 {
		t.Errorf("expressions = %+v", seg.Expressions)
	}
	// The gesture was written before everything, so it fires with the first spoken character.
	if len(seg.Gestures) != 1 || seg.Gestures[0].Name != "waving" || !approx(seg.Gestures[0].At, 0.89) {
		t.Errorf("gestures = %+v", seg.Gestures)
	}
	if !approx(seg.Duration, sp.Duration) {
		t.Errorf("duration = %v want %v", seg.Duration, sp.Duration)
	}
	for i := 1; i < len(seg.Words); i++ {
		if seg.Words[i].Start < seg.Words[i-1].End-0.0001 {
			t.Errorf("words overlap: %+v", seg.Words)
		}
	}
}

func TestBuildGestureMidSentence(t *testing.T) {
	tagged := Parse("Hello there, {thankful} thank you.", vocab)
	sp := speechFor(tagged.TTSText, 0, 0, 0.1)
	seg := Build(0, tagged, sp)
	// "Hello there, " is 13 characters, so "t" of "thank" starts at 1.3 s.
	if len(seg.Gestures) != 1 || !approx(seg.Gestures[0].At, 1.3) {
		t.Errorf("gestures = %+v", seg.Gestures)
	}
}

func TestBuildMidExpression(t *testing.T) {
	tagged := Parse("Well [sighs] okay then.", vocab)
	sp := speechFor(tagged.TTSText, 0, 0, 0.1)
	seg := Build(0, tagged, sp)
	if len(seg.Expressions) != 1 || !approx(seg.Expressions[0].At, 0.5) { // "Well " = 5 chars
		t.Errorf("expressions = %+v", seg.Expressions)
	}
	if len(seg.Words) != 3 || seg.Words[2].Text != "then." {
		t.Fatalf("words = %+v", seg.Words)
	}
	// "okay" follows the tag: "Well [sighs] " is 13 characters.
	if !approx(seg.Words[1].Start, 1.3) {
		t.Errorf("okay = %+v", seg.Words[1])
	}
}

func TestBuildAlignmentMismatchFallsBackToProportional(t *testing.T) {
	tagged := Parse("{waving} Twenty past three.", vocab)
	// The provider spoke a normalised text ("20 past 3.") so the alignment differs.
	sp := speechFor("20 past 3.", 0, 0, 0.2)
	seg := Build(0, tagged, sp)
	if seg.Words == nil || len(seg.Words) != 3 || seg.AudioB64 == nil {
		t.Fatalf("seg = %+v", seg)
	}
	if !approx(seg.Duration, 2.0) || seg.Words[0].Start != 0 || !approx(seg.Words[2].End, 2.0) {
		t.Errorf("not proportional: %+v dur %v", seg.Words, seg.Duration)
	}
	for _, w := range seg.Words {
		if w.End < w.Start || w.End > seg.Duration+0.001 {
			t.Errorf("bad word %+v", w)
		}
	}
	if len(seg.Gestures) != 1 || seg.Gestures[0].At != 0 {
		t.Errorf("gestures = %+v", seg.Gestures)
	}
}

func TestBuildBrokenAlignmentsNeverPanic(t *testing.T) {
	tagged := Parse("{waving} [laughs] Hello there, friend.", vocab)
	bad := []voice.Alignment{
		{},
		{Chars: []string{"a", "b"}, Starts: []float64{0}, Ends: []float64{1, 2}},
		{Chars: []string{"[laughs] Hello there, friend."}, Starts: []float64{0}, Ends: []float64{1}},
		{Chars: strings.Split(tagged.TTSText+"extra", ""), Starts: nil, Ends: nil},
	}
	for i, a := range bad {
		seg := Build(0, tagged, &voice.Speech{Audio: []byte{1}, Alignment: a, Duration: 2})
		if seg.Duration != 2 || seg.AudioFormat == nil || *seg.AudioFormat != "mp3" {
			t.Errorf("case %d: %+v", i, seg)
		}
	}
	// Hand-built Tagged with offsets out of range.
	weird := Tagged{Display: "ab", TTSText: "ab", Gestures: []Mark{{"waving", 99}}, Expressions: []Mark{{"laughs", 50}}}
	_ = Build(0, weird, speechFor("ab", 0, 0, 0.1))
	_ = Build(0, Tagged{}, speechFor("", 0, 0, 0.1))
	_ = Build(0, Tagged{}, nil)
}

func TestBuildWithoutSpeech(t *testing.T) {
	tagged := Parse("Hi {waving} there. [laughs] This is a longer sentence to read.", vocab)
	seg := Build(2, tagged, nil)
	wantDur := float64(len([]rune(tagged.Display))) / 15
	if !approx(seg.Duration, wantDur) {
		t.Errorf("duration = %v want %v", seg.Duration, wantDur)
	}
	if seg.AudioB64 != nil || seg.AudioFormat != nil || seg.Words != nil {
		t.Errorf("audio/words must be null: %+v", seg)
	}
	if len(seg.Gestures) != 1 || !approx(seg.Gestures[0].At, wantDur*3/float64(len([]rune(tagged.Display)))) {
		t.Errorf("gesture = %+v", seg.Gestures)
	}
	// "[laughs]" sits before "This": 'Hi there. ' is 10 display characters.
	if len(seg.Expressions) != 1 || !approx(seg.Expressions[0].At, wantDur*10/float64(len([]rune(tagged.Display)))) {
		t.Errorf("expression = %+v (display %q)", seg.Expressions, tagged.Display)
	}

	if short := Build(0, Parse("Hi.", vocab), nil); short.Duration != 1.2 {
		t.Errorf("minimum duration = %v", short.Duration)
	}
}

func TestSegmentJSONShape(t *testing.T) {
	b, err := json.Marshal(Build(0, Parse("Hi.", vocab), nil))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":0,"text":"Hi.","duration":1.2,"audio_b64":null,"audio_format":null,"words":null,"gestures":[],"expressions":[]}`
	if string(b) != want {
		t.Errorf("json = %s\nwant   %s", b, want)
	}
}

// The first sentence is cut early at a soft break so speech starts sooner;
// later sentences may run to the normal limit.
func TestSplitterCutsFirstSentenceEarly(t *testing.T) {
	long := "I'd like to, Ember, but I don't have a way to set reminders or keep a grocery list yet — nothing to write it in, so I won't pretend it's saved."
	var s Splitter
	got := append(s.Push(long+" "+long+" "), s.Flush()...)
	want := []string{
		"I'd like to, Ember,",
		"but I don't have a way to set reminders or keep a grocery list yet — nothing to write it in, so I won't pretend it's saved.",
		long,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}
