package parakeet

import (
	"errors"
	"fmt"
	"math"
)

const (
	modelSampleRate = 16000
	minSamples      = modelSampleRate / 10 // under 100 ms there is nothing to transcribe
	maxSeconds      = 300                  // push-to-talk clips are seconds long; bound memory and decode time
	silencePeak     = 0.004                // about -48 dBFS; below this the clip is noise floor
)

// pcmToFloat32 converts 16-bit PCM to floats in [-1, 1).
func pcmToFloat32(pcm []int16) []float32 {
	out := make([]float32, len(pcm))
	for i, s := range pcm {
		out[i] = float32(s) / 32768
	}
	return out
}

// resample converts mono samples from one rate to another with linear
// interpolation. Good enough for speech going into a recognizer; the client
// already sends 16 kHz, so this is only a safety net.
func resample(in []float32, from, to int) []float32 {
	if from == to || len(in) == 0 {
		return in
	}
	n := int(math.Round(float64(len(in)) * float64(to) / float64(from)))
	if n < 1 {
		n = 1
	}
	out := make([]float32, n)
	step := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		frac := float32(pos - float64(j))
		out[i] = in[j]*(1-frac) + in[j+1]*frac
	}
	return out
}

// prepare validates the clip and returns 16 kHz float samples, or nil when the
// clip is too short or silent to be worth decoding.
func prepare(pcm []int16, sampleRate int) ([]float32, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("parakeet: invalid sample rate %d", sampleRate)
	}
	if len(pcm) > maxSeconds*sampleRate {
		return nil, errors.New("parakeet: audio too long")
	}
	// Cheap checks on the original samples first: no allocation for silence.
	if len(pcm) < sampleRate/10 {
		return nil, nil
	}
	var peak int32
	for _, s := range pcm {
		peak = max(peak, abs32(int32(s)))
	}
	if float32(peak)/32768 < silencePeak {
		return nil, nil
	}
	samples := resample(pcmToFloat32(pcm), sampleRate, modelSampleRate)
	if len(samples) < minSamples {
		return nil, nil
	}
	return samples, nil
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
