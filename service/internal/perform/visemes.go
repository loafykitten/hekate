package perform

import (
	"strings"
	"unicode"
)

// Viseme is a mouth shape that starts At seconds into the segment. V is one of
// the 15 Oculus viseme classes (sil, PP, FF, TH, DD, kk, CH, SS, nn, RR, aa,
// E, I, O, U); the character's manifest maps each to its own morphs.
type Viseme struct {
	V  string  `json:"v"`
	At float64 `json:"at"`
}

// A pause between words longer than this closes the mouth.
const visemePause = 0.08

// digraphs are letter pairs spoken as one sound, checked before single letters.
// An empty class means the pair makes no shape of its own (it's silent, or
// just breath): the mouth holds the shape before it.
var digraphs = map[string]string{
	"th": "TH", "sh": "CH", "ch": "CH", "ph": "FF", "ck": "kk", "ng": "nn", "wh": "U", "qu": "kk",
	"gh": "", "ee": "I", "ea": "I", "ie": "I", "oo": "U", "ou": "O", "ow": "O", "oa": "O", "ai": "E", "ay": "E",
}

// letters are single English letters and their usual shape.
var letters = map[rune]string{
	'a': "aa", 'e': "E", 'i': "I", 'o': "O", 'u': "U", 'y': "I",
	'b': "PP", 'm': "PP", 'p': "PP", 'f': "FF", 'v': "FF", 't': "DD", 'd': "DD",
	's': "SS", 'z': "SS", 'c': "kk", 'k': "kk", 'g': "kk", 'q': "kk", 'x': "kk",
	'j': "CH", 'l': "nn", 'n': "nn", 'r': "RR", 'w': "U", 'h': "",
}

// spelled is one sound read from a word: its class and the rune it starts at.
type spelled struct {
	v   string
	pos int
}

// spell reads a word's letters as English sounds, by rule of thumb: good
// enough for a mouth, not a pronunciation. ok is false when the word holds
// something the rules can't read (a digit, a letter from another alphabet):
// how it's spoken can't be guessed from its letters.
func spell(word []rune) (out []spelled, ok bool) {
	lower := []rune(strings.ToLower(string(word)))
	last := len(lower) - 1 // the last letter, past any punctuation
	for last >= 0 && !unicode.IsLetter(lower[last]) {
		if unicode.IsDigit(lower[last]) {
			return nil, false
		}
		last--
	}
	for i := 0; i < len(lower); {
		r := lower[i]
		if unicode.IsDigit(r) {
			return nil, false
		}
		if !unicode.IsLetter(r) {
			i++
			continue
		}
		if i+1 < len(lower) {
			if v, ok := digraphs[string(lower[i:i+2])]; ok {
				if v != "" {
					out = append(out, spelled{v, i})
				}
				i += 2
				continue
			}
		}
		v, known := letters[r]
		switch {
		case !known:
			return nil, false
		case r == 'c' && i+1 < len(lower) && strings.ContainsRune("eiy", lower[i+1]):
			v = "SS" // a soft c, as in "nice"
		case r == 'e' && i == last && i >= 3 && !isVowel(lower[i-1]):
			v = "" // a silent final e, as in "take" (but not "the" or "she")
		}
		if v != "" {
			out = append(out, spelled{v, i})
		}
		i++
	}
	return out, true
}

func isVowel(r rune) bool { return strings.ContainsRune("aeiouy", r) }

// visemes lays the display text's sounds on the speech's timeline: each starts
// with the first character spelling it, and the mouth closes in the pauses
// between words and at the end. at(i) and end(i) are the timings of display
// rune i. A line with a word the rules can't read gets no shapes at all, so
// the mouth follows loudness for the whole line rather than staying shut
// while that word is said.
func visemes(display []rune, at, end func(int) float64) []Viseme {
	out := []Viseme{}
	add := func(v string, t float64) {
		t = round3(t)
		if n := len(out); n > 0 {
			if out[n-1].V == v {
				return
			}
			if out[n-1].At >= t {
				out[n-1] = Viseme{v, out[n-1].At} // two sounds in one instant: the later wins
				return
			}
		}
		out = append(out, Viseme{v, t})
	}
	lastEnd := -1.0
	for _, w := range splitWords(display) {
		sounds, ok := spell(display[w.a:w.b])
		if !ok {
			return []Viseme{}
		}
		if len(sounds) == 0 {
			continue
		}
		start := at(w.a + sounds[0].pos)
		if lastEnd >= 0 && start-lastEnd > visemePause {
			add("sil", lastEnd)
		}
		for _, s := range sounds {
			add(s.v, at(w.a+s.pos))
		}
		lastEnd = end(w.b - 1)
	}
	if lastEnd >= 0 {
		add("sil", lastEnd)
	}
	return out
}
