// Package voice defines speech in and out. Implementations live in subpackages
// (elevenlabs for text-to-speech, parakeet for speech-to-text); the agent only
// depends on these interfaces. A nil implementation means "not configured".
package voice

import (
	"context"
	"errors"
)

// ErrNotReady is returned while an implementation is still starting up, e.g.
// the speech-to-text model is downloading.
var ErrNotReady = errors.New("voice: not ready")

// Alignment is per-character timing for exactly the text that was synthesized,
// including any [expression] tags (ElevenLabs reports them as characters that
// span the time the sound takes).
type Alignment struct {
	Chars  []string  // one entry per character, in order
	Starts []float64 // seconds from the start of the audio
	Ends   []float64
}

// Speech is synthesized audio for one sentence.
type Speech struct {
	Audio     []byte
	Format    string // "mp3"
	Duration  float64
	Alignment Alignment
}

// Synthesizer turns text (which may contain [expression] tags) into speech.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (*Speech, error)
}

// Transcriber turns 16-bit mono PCM into text. It returns ErrNotReady until
// it can transcribe; Ready reports the same without doing work.
type Transcriber interface {
	Transcribe(ctx context.Context, pcm []int16, sampleRate int) (string, error)
	Ready() bool
}
