// Package parakeet implements voice.Transcriber with NVIDIA Parakeet TDT 0.6B v2
// (English, int8) running in-process through sherpa-onnx. The model (about
// 640 MB) is downloaded on first start, in the background; until it is loaded,
// Transcribe returns voice.ErrNotReady.
package parakeet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/loafykitten/hekate/service/internal/voice"
)

var _ voice.Transcriber = (*Transcriber)(nil)

// engine is the decoder behind Transcribe; sherpa-onnx in production, a fake in tests.
type engine interface {
	decode(samples []float32) (string, error)
	close()
}

// Transcriber is safe for concurrent use; decodes run one at a time.
type Transcriber struct {
	log           *slog.Logger
	modelsDir     string
	archiveURL    string
	archiveSHA256 string
	http          *http.Client
	threads       int
	progressEvery time.Duration
	retryBase     time.Duration
	retryMax      time.Duration
	load          func(dir string, threads int) (engine, error)

	ready atomic.Bool
	// slot serializes decodes (and Close) while letting a waiting caller give up.
	slot   chan struct{}
	eng    engine // guarded by slot
	closed bool   // guarded by slot
	done   chan struct{}
}

// Option customizes a Transcriber.
type Option func(*Transcriber)

// WithThreads sets the CPU threads used for decoding (default 4).
func WithThreads(n int) Option { return func(t *Transcriber) { t.threads = n } }

// WithHTTPClient replaces the client used for the model download.
func WithHTTPClient(h *http.Client) Option { return func(t *Transcriber) { t.http = h } }

// New returns immediately and prepares the model in the background: it
// downloads and verifies it if needed, then loads the recognizer. Ready turns
// true when that is done. The background work stops when ctx is cancelled.
func New(ctx context.Context, modelsDir string, log *slog.Logger, opts ...Option) *Transcriber {
	t := newTranscriber(modelsDir, log, opts...)
	go t.prepare(ctx)
	return t
}

func newTranscriber(modelsDir string, log *slog.Logger, opts ...Option) *Transcriber {
	if log == nil {
		log = slog.Default()
	}
	t := &Transcriber{
		log:           log,
		modelsDir:     modelsDir,
		archiveURL:    defaultArchiveURL,
		archiveSHA256: defaultArchiveSHA256,
		http:          &http.Client{},
		threads:       4,
		progressEvery: 10 * time.Second,
		retryBase:     5 * time.Second,
		retryMax:      5 * time.Minute,
		load:          loadSherpa,
		slot:          make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
	t.slot <- struct{}{}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Ready reports whether Transcribe can run.
func (t *Transcriber) Ready() bool { return t.ready.Load() }

// Done is closed once the background preparation has finished (successfully,
// or because the context was cancelled).
func (t *Transcriber) Done() <-chan struct{} { return t.done }

// Close frees the recognizer. Transcribe returns ErrNotReady afterwards.
func (t *Transcriber) Close() {
	<-t.slot
	t.closed = true
	t.ready.Store(false)
	if t.eng != nil {
		t.eng.close()
		t.eng = nil
	}
	t.slot <- struct{}{}
}

func (t *Transcriber) prepare(ctx context.Context) {
	defer close(t.done)
	dir := filepath.Join(t.modelsDir, ModelDirName)
	backoff := t.retryBase
	for !modelComplete(dir) {
		t.log.Info("speech model missing, downloading", "dir", dir)
		start := time.Now()
		err := t.download(ctx, dir)
		if err == nil {
			t.log.Info("speech model downloaded", "took", time.Since(start).Round(time.Second))
			break
		}
		if ctx.Err() != nil {
			return
		}
		t.log.Error("speech model download failed", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, t.retryMax)
	}

	start := time.Now()
	eng, err := t.load(dir, t.threads)
	if err != nil {
		// A load failure on a verified model won't fix itself; say so loudly.
		t.log.Error("speech model failed to load; speech-to-text is unavailable", "err", err)
		return
	}
	select {
	case <-t.slot:
	case <-ctx.Done():
		eng.close()
		return
	}
	if t.closed {
		eng.close()
		t.slot <- struct{}{}
		return
	}
	t.eng = eng
	t.ready.Store(true)
	t.slot <- struct{}{}
	t.log.Info("speech-to-text ready", "model", ModelDirName, "load", time.Since(start).Round(time.Millisecond))
}

// Transcribe turns mono 16-bit PCM into text. Very short or silent clips
// return "" without touching the model.
func (t *Transcriber) Transcribe(ctx context.Context, pcm []int16, sampleRate int) (string, error) {
	if !t.ready.Load() {
		return "", voice.ErrNotReady
	}
	samples, err := prepare(pcm, sampleRate)
	if err != nil {
		return "", err
	}
	if samples == nil {
		return "", nil
	}
	select {
	case <-t.slot:
	case <-ctx.Done():
		return "", fmt.Errorf("parakeet: %w", ctx.Err())
	}
	defer func() { t.slot <- struct{}{} }()
	if t.eng == nil { // closed while we waited
		return "", voice.ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("parakeet: %w", err)
	}
	start := time.Now()
	text, err := t.eng.decode(samples)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	t.log.Debug("transcribed", "audio_s", float64(len(samples))/modelSampleRate, "took", time.Since(start).Round(time.Millisecond), "chars", len(text))
	return text, nil
}

type sherpaEngine struct{ r *sherpa.OfflineRecognizer }

func loadSherpa(dir string, threads int) (engine, error) {
	cfg := sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: modelSampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OfflineModelConfig{
			Transducer: sherpa.OfflineTransducerModelConfig{
				Encoder: filepath.Join(dir, encoderFile),
				Decoder: filepath.Join(dir, decoderFile),
				Joiner:  filepath.Join(dir, joinerFile),
			},
			Tokens:     filepath.Join(dir, tokensFile),
			NumThreads: threads,
			Provider:   "cpu",
			// sherpa-onnx picks the NeMo TDT decoder from this.
			ModelType: "nemo_transducer",
		},
		DecodingMethod: "greedy_search",
	}
	r := sherpa.NewOfflineRecognizer(&cfg)
	if r == nil {
		return nil, errors.New("sherpa-onnx could not create the recognizer (model files unreadable?)")
	}
	return &sherpaEngine{r: r}, nil
}

// decode uses a fresh stream per call, which is sherpa-onnx's documented
// pattern; the recognizer itself is shared.
func (e *sherpaEngine) decode(samples []float32) (string, error) {
	s := sherpa.NewOfflineStream(e.r)
	defer sherpa.DeleteOfflineStream(s)
	s.AcceptWaveform(modelSampleRate, samples)
	e.r.Decode(s)
	return s.GetResult().Text, nil
}

func (e *sherpaEngine) close() { sherpa.DeleteOfflineRecognizer(e.r) }
