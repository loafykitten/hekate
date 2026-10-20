package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/perform"
	"github.com/loafykitten/hekate/service/internal/voice"
)

const (
	synthConcurrency = 3  // sentences synthesizing at once
	jobBuffer        = 32 // parsed sentences waiting to be emitted
)

// segRecord remembers one emitted segment so an interrupted reply can be
// trimmed to what was heard.
type segRecord struct {
	seq     int
	round   int
	raw     string // the sentence as the model wrote it, tags included
	display string
}

type job struct {
	round  int
	raw    string
	tagged perform.Tagged
	speech *voice.Speech
	done   chan struct{} // closed when speech is ready (or failed)
}

// pipeline turns sentences into ordered segments: sentence n+1 synthesizes
// while sentence n is being sent. Submit and close are called from one
// goroutine; the emitter goroutine owns segment numbering.
type pipeline struct {
	ctx    context.Context
	synth  voice.Synthesizer // nil: text-only
	vocab  perform.Vocabulary
	emit   func(perform.Segment)
	log    *slog.Logger
	jobs   chan *job
	sem    chan struct{}
	synthW sync.WaitGroup
	emitW  sync.WaitGroup

	mu      sync.Mutex
	records []segRecord
	playEnd time.Time // when a client playing the segments as they arrive would finish
	closed  bool
}

func newPipeline(ctx context.Context, synth voice.Synthesizer, vocab perform.Vocabulary,
	emit func(perform.Segment), log *slog.Logger) *pipeline {
	p := &pipeline{
		ctx: ctx, synth: synth, vocab: vocab, emit: emit, log: log,
		jobs: make(chan *job, jobBuffer),
		sem:  make(chan struct{}, synthConcurrency),
	}
	p.emitW.Add(1)
	go p.run()
	return p
}

// submit parses one sentence and queues it. It blocks while the synthesizer is
// saturated, which paces the model instead of buffering without bound.
func (p *pipeline) submit(round int, sentence string) {
	tagged := perform.Parse(sentence, p.vocab)
	if len(tagged.Dropped) > 0 {
		p.log.Warn("dropped unknown tags", "tags", tagged.Dropped)
	}
	if tagged.Display == "" {
		return // nothing to say (a tag on its own); the next sentence carries on
	}
	j := &job{round: round, raw: sentence, tagged: tagged, done: make(chan struct{})}
	if p.synth == nil {
		close(j.done)
	} else {
		select {
		case p.sem <- struct{}{}:
		case <-p.ctx.Done():
			return
		}
		p.synthW.Add(1)
		go func() {
			defer p.synthW.Done()
			defer close(j.done)
			defer func() { <-p.sem }()
			sp, err := p.synth.Synthesize(p.ctx, tagged.TTSText)
			if err != nil {
				if p.ctx.Err() == nil {
					p.log.Warn("speech synthesis failed, sending text only", "error", err)
				}
				return
			}
			j.speech = sp
		}()
	}
	select {
	case p.jobs <- j:
	case <-p.ctx.Done():
	}
}

func (p *pipeline) run() {
	defer p.emitW.Done()
	seq := 0
	for j := range p.jobs {
		select {
		case <-j.done:
		case <-p.ctx.Done():
			return
		}
		if p.ctx.Err() != nil {
			return // interrupted: say nothing more
		}
		seg := perform.Build(seq, j.tagged, j.speech)
		p.emit(seg)
		p.mu.Lock()
		p.records = append(p.records, segRecord{seq: seq, round: j.round, raw: j.raw, display: j.tagged.Display})
		p.playEnd = later(p.playEnd, time.Now()).Add(time.Duration(seg.Duration*float64(time.Second)) + playGap)
		p.mu.Unlock()
		seq++
	}
}

// finish closes the queue and waits until everything submitted was emitted or
// the context ended.
func (p *pipeline) finish() {
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.emitW.Wait()
	p.synthW.Wait()
}

// The app leaves a short pause before each sentence (its lead and the gap
// between queued ones, about 150 ms), and delivery, decoding and the output
// device add some more. Playback estimates err long by these.
const (
	playGap   = 250 * time.Millisecond
	playSlack = 2 * time.Second
)

// playsUntil estimates, generously, when a client playing what was emitted as
// it arrived would be done.
func (p *pipeline) playsUntil() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playEnd.IsZero() {
		return p.playEnd
	}
	return p.playEnd.Add(playSlack)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (p *pipeline) segments() []segRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]segRecord(nil), p.records...)
}
