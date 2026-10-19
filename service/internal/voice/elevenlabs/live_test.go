package elevenlabs

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSynthesize hits the real API; it costs a few credits, so it only
// runs with HEKATE_LIVE_TESTS=1 and ELEVENLABS_API_KEY set.
func TestLiveSynthesize(t *testing.T) {
	key := os.Getenv("ELEVENLABS_API_KEY")
	if os.Getenv("HEKATE_LIVE_TESTS") != "1" || key == "" {
		t.Skip("set HEKATE_LIVE_TESTS=1 and ELEVENLABS_API_KEY to run")
	}
	voiceID := envOr("ELEVENLABS_VOICE_ID", "EXAVITQu4vr4xnSDxMaL")
	model := envOr("ELEVENLABS_MODEL", "eleven_v4")
	const text = "[chuckles] Good morning. You have two things due today."

	c := New(key, voiceID, model)
	start := time.Now()
	sp, err := c.Synthesize(t.Context(), text)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Audio) < 1000 || sp.Format != "mp3" {
		t.Fatalf("audio %d bytes, format %q", len(sp.Audio), sp.Format)
	}
	if got := strings.Join(sp.Alignment.Chars, ""); got != text {
		t.Fatalf("alignment text %q != input %q", got, text)
	}
	n := len(sp.Alignment.Chars)
	for i := 1; i < n; i++ {
		if sp.Alignment.Starts[i] < sp.Alignment.Starts[i-1] {
			t.Fatalf("starts not monotonic at %d", i)
		}
	}
	t.Logf("model=%s latency=%v audio=%dB duration=%.2fs chars=%d tag=[0..%.2fs]",
		model, took.Round(time.Millisecond), len(sp.Audio), sp.Duration, n, sp.Alignment.Ends[len("[chuckles]")-1])
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
