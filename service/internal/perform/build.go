package perform

import (
	"encoding/base64"
	"math"
	"strings"
	"unicode"

	"github.com/loafykitten/hekate/service/internal/voice"
)

// Reading speed used when there is no audio: about 15 characters a second,
// never shorter than 1.2 s.
const (
	readRunesPerSecond = 15.0
	minReadSeconds     = 1.2
)

// Word is a display word with its time inside the segment.
type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Gesture fires at At seconds into the segment.
type Gesture struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
}

// Expression is an ElevenLabs tag, for driving the face.
type Expression struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
}

// Segment is the body of a PROTOCOL.md segment message, without type and turn_id.
type Segment struct {
	Seq         int          `json:"seq"`
	Text        string       `json:"text"`
	Duration    float64      `json:"duration"`
	AudioB64    *string      `json:"audio_b64"`
	AudioFormat *string      `json:"audio_format"`
	Words       []Word       `json:"words"`
	Gestures    []Gesture    `json:"gestures"`
	Expressions []Expression `json:"expressions"`
}

// Build maps a tagged sentence onto time. sp may be nil (no voice).
//
// With speech, the alignment describes TTSText, tags included (a tag spans the
// time the sound takes), so display characters are looked up through their
// position in TTSText. If the alignment does not match TTSText, because the
// provider normalised the text, timings fall back to proportional.
func Build(seq int, t Tagged, sp *voice.Speech) Segment {
	display := []rune(t.Display)
	seg := Segment{Seq: seq, Text: t.Display, Gestures: []Gesture{}, Expressions: []Expression{}}

	d2t := displayToTTS(t)
	hasAudio := sp != nil && len(sp.Audio) > 0
	var times *runeTimes
	if hasAudio {
		times = alignedTimes(sp.Alignment, t.TTSText)
		b64 := base64.StdEncoding.EncodeToString(sp.Audio)
		seg.AudioB64 = &b64
		format := sp.Format
		if format == "" {
			format = "mp3"
		}
		seg.AudioFormat = &format
		seg.Duration = sp.Duration
		if seg.Duration <= 0 {
			seg.Duration = lastEnd(sp.Alignment)
		}
		seg.Duration = max(seg.Duration, 0.1)
	} else {
		seg.Duration = max(minReadSeconds, float64(len(display))/readRunesPerSecond)
	}
	seg.Duration = round3(seg.Duration)

	// Proportional position of a display offset, used without audio and when
	// the alignment is unusable.
	prop := func(displayOffset int) float64 {
		if len(display) == 0 {
			return 0
		}
		return seg.Duration * float64(displayOffset) / float64(len(display))
	}
	// displayPos is how many display characters precede a TTSText offset.
	displayPos := func(ttsOffset int) int {
		n := 0
		for _, i := range d2t {
			if i >= ttsOffset {
				break
			}
			n++
		}
		return n
	}

	exact := times != nil && len(d2t) == len(display)
	if hasAudio {
		seg.Words = []Word{} // with audio, "no words" is an empty list, not null
		for _, w := range splitWords(display) {
			word := Word{Text: string(display[w.a:w.b]), Start: prop(w.a), End: prop(w.b)}
			if exact {
				word.Start, word.End = times.starts[d2t[w.a]], times.ends[d2t[w.b-1]]
			}
			word.Start, word.End = round3(word.Start), round3(word.End)
			seg.Words = append(seg.Words, word)
		}
	}

	for _, g := range t.Gestures {
		at := prop(g.Offset)
		if exact {
			at = displayStart(times, d2t, g.Offset, seg.Duration)
		}
		seg.Gestures = append(seg.Gestures, Gesture{Name: g.Name, At: round3(clampTime(at, seg.Duration))})
	}
	for _, e := range t.Expressions {
		at := prop(displayPos(e.Offset))
		if exact && e.Offset < len(times.starts) {
			at = times.starts[e.Offset]
		}
		seg.Expressions = append(seg.Expressions, Expression{Name: e.Name, At: round3(clampTime(at, seg.Duration))})
	}
	return seg
}

// displayStart is the start time of the display character at offset, or the
// end of the last character when the offset is past the end.
func displayStart(times *runeTimes, d2t []int, offset int, dur float64) float64 {
	if offset < len(d2t) {
		return times.starts[d2t[offset]]
	}
	if len(d2t) > 0 {
		return times.ends[d2t[len(d2t)-1]]
	}
	return dur
}

type runeTimes struct{ starts, ends []float64 }

// alignedTimes expands the provider's per-character timings to one entry per
// rune of want. It returns nil when the alignment does not describe want.
func alignedTimes(a voice.Alignment, want string) *runeTimes {
	if len(a.Chars) == 0 || len(a.Chars) != len(a.Starts) || len(a.Chars) != len(a.Ends) {
		return nil
	}
	var rt runeTimes
	var got strings.Builder
	for i, c := range a.Chars {
		for _, r := range c {
			got.WriteRune(r)
			rt.starts = append(rt.starts, a.Starts[i])
			rt.ends = append(rt.ends, a.Ends[i])
		}
	}
	if got.String() != want {
		return nil
	}
	return &rt
}

func lastEnd(a voice.Alignment) float64 {
	if len(a.Ends) == 0 {
		return 0
	}
	return a.Ends[len(a.Ends)-1]
}

// displayToTTS maps each display rune to its index in TTSText by walking both
// texts and skipping the [expression] tag spans.
func displayToTTS(t Tagged) []int {
	display, tts := []rune(t.Display), []rune(t.TTSText)
	skip := map[int]int{} // tag start -> tag length
	for _, e := range t.Expressions {
		skip[e.Offset] = len([]rune(e.Name)) + 2
	}
	out := make([]int, 0, len(display))
	for i := 0; i < len(tts) && len(out) < len(display); {
		if n, ok := skip[i]; ok {
			i += n
			continue
		}
		if tts[i] == display[len(out)] {
			out = append(out, i)
		}
		i++
	}
	return out
}

type span struct{ a, b int }

// splitWords returns rune ranges of whitespace-separated tokens.
func splitWords(rs []rune) []span {
	var out []span
	start := -1
	for i, r := range rs {
		switch {
		case unicode.IsSpace(r) && start >= 0:
			out = append(out, span{start, i})
			start = -1
		case !unicode.IsSpace(r) && start < 0:
			start = i
		}
	}
	if start >= 0 {
		out = append(out, span{start, len(rs)})
	}
	return out
}

func clampTime(at, dur float64) float64 { return math.Max(0, math.Min(at, dur)) }

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
