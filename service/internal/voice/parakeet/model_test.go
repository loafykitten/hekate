package parakeet

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealModel transcribes a WAV with the real model. It runs only when
// HEKATE_STT_MODEL_DIR points at the folder that holds the model directory
// (the same folder as HEKATE_MODELS_DIR). HEKATE_STT_TEST_WAV may name a
// 16 kHz mono 16-bit WAV; otherwise the sample bundled with the model is used.
func TestRealModel(t *testing.T) {
	models := os.Getenv("HEKATE_STT_MODEL_DIR")
	if models == "" {
		t.Skip("set HEKATE_STT_MODEL_DIR to run against the real model")
	}
	dir := filepath.Join(models, ModelDirName)
	if !modelComplete(dir) {
		t.Skipf("no complete model in %s", dir)
	}
	wav := os.Getenv("HEKATE_STT_TEST_WAV")
	if wav == "" {
		t.Skip("set HEKATE_STT_TEST_WAV to a 16 kHz mono 16-bit WAV")
	}
	pcm, rate, err := ReadWAV(wav)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	tr := New(ctx, models, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer tr.Close()
	select {
	case <-tr.Done():
	case <-ctx.Done():
		t.Fatal("model did not load")
	}
	if !tr.Ready() {
		t.Fatal("not ready")
	}
	for i := range 2 { // first call warms up
		start := time.Now()
		text, err := tr.Transcribe(ctx, pcm, rate)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("run %d: %q (%v for %.1fs of audio)", i, text, time.Since(start).Round(time.Millisecond), float64(len(pcm))/float64(rate))
		if want := os.Getenv("HEKATE_STT_EXPECT"); want != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("transcript %q does not contain %q", text, want)
		}
	}
	// Silence returns instantly and empty.
	if text, err := tr.Transcribe(ctx, make([]int16, 32000), 16000); err != nil || text != "" {
		t.Errorf("silence = %q, %v", text, err)
	}
}

func TestReadWAV(t *testing.T) {
	// 44-byte header + 4 samples, with an extra LIST chunk that must be skipped.
	var b []byte
	le := binary.LittleEndian
	chunk := func(id string, body []byte) {
		b = append(b, id...)
		b = le.AppendUint32(b, uint32(len(body)))
		b = append(b, body...)
		if len(body)%2 == 1 {
			b = append(b, 0)
		}
	}
	fmtBody := le.AppendUint16(nil, 1)
	fmtBody = le.AppendUint16(fmtBody, 1)
	fmtBody = le.AppendUint32(fmtBody, 16000)
	fmtBody = le.AppendUint32(fmtBody, 32000)
	fmtBody = le.AppendUint16(fmtBody, 2)
	fmtBody = le.AppendUint16(fmtBody, 16)
	var data []byte
	for _, s := range []int16{1, -2, 300, -400} {
		data = le.AppendUint16(data, uint16(s))
	}
	b = append(b, "RIFF\x00\x00\x00\x00WAVE"...)
	chunk("LIST", []byte("abc"))
	chunk("fmt ", fmtBody)
	chunk("data", data)

	p := filepath.Join(t.TempDir(), "x.wav")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	pcm, rate, err := ReadWAV(p)
	if err != nil || rate != 16000 || len(pcm) != 4 || pcm[2] != 300 || pcm[3] != -400 {
		t.Fatalf("pcm=%v rate=%d err=%v", pcm, rate, err)
	}
	if err := os.WriteFile(p, []byte("not a wav file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadWAV(p); err == nil {
		t.Error("garbage accepted")
	}
}
