package parakeet

import (
	"math"
	"testing"
)

func TestPCMToFloat32(t *testing.T) {
	got := pcmToFloat32([]int16{0, 16384, -16384, 32767, -32768})
	want := []float32{0, 0.5, -0.5, 32767.0 / 32768, -1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	for _, v := range got {
		if v < -1 || v > 1 {
			t.Errorf("%v outside [-1, 1]", v)
		}
	}
}

func TestResample(t *testing.T) {
	in := []float32{0, 1, 2, 3}
	if got := resample(in, 16000, 16000); len(got) != 4 {
		t.Errorf("same rate changed length: %v", got)
	}
	// 8 kHz -> 16 kHz: twice as many samples, midpoints interpolated.
	up := resample(in, 8000, 16000)
	if len(up) != 8 || up[0] != 0 || up[1] != 0.5 || up[2] != 1 || up[7] != 3 {
		t.Errorf("upsample = %v", up)
	}
	// 48 kHz -> 16 kHz: a third of the samples, taking every third.
	long := make([]float32, 480)
	for i := range long {
		long[i] = float32(i)
	}
	down := resample(long, 48000, 16000)
	if len(down) != 160 || down[10] != 30 {
		t.Errorf("downsample len=%d [10]=%v", len(down), down[10])
	}
	// A 440 Hz sine keeps its frequency: count zero crossings over 1 s.
	sine := make([]float32, 44100)
	for i := range sine {
		sine[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / 44100))
	}
	out := resample(sine, 44100, 16000)
	if len(out) != 16000 {
		t.Fatalf("len = %d", len(out))
	}
	crossings := 0
	for i := 1; i < len(out); i++ {
		if (out[i-1] < 0) != (out[i] < 0) {
			crossings++
		}
	}
	if crossings < 875 || crossings > 885 {
		t.Errorf("zero crossings = %d, want about 880", crossings)
	}
}

func TestPrepare(t *testing.T) {
	loud := make([]int16, 16000)
	for i := range loud {
		loud[i] = int16(8000 * math.Sin(float64(i)/10))
	}
	if s, err := prepare(loud, 16000); err != nil || len(s) != 16000 {
		t.Errorf("loud: len=%d err=%v", len(s), err)
	}
	if s, err := prepare(loud[:8000], 8000); err != nil || len(s) != 16000 {
		t.Errorf("8 kHz: len=%d err=%v", len(s), err)
	}
	if s, err := prepare(make([]int16, 16000), 16000); err != nil || s != nil {
		t.Errorf("silence: %v %v", len(s), err)
	}
	if s, err := prepare(loud[:800], 16000); err != nil || s != nil {
		t.Errorf("too short: %v %v", len(s), err)
	}
	if s, err := prepare(nil, 16000); err != nil || s != nil {
		t.Errorf("empty: %v %v", len(s), err)
	}
	if _, err := prepare(loud, 0); err == nil {
		t.Error("zero sample rate accepted")
	}
	if _, err := prepare(make([]int16, 301*16000), 16000); err == nil {
		t.Error("5+ minute clip accepted")
	}
}
