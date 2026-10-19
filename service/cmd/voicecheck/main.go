// Command voicecheck is a dev tool for trying the speech implementations
// without the rest of the service.
//
//	voicecheck tts "text with [laughs] tags" out.mp3
//	voicecheck stt in.wav
//
// tts reads ELEVENLABS_API_KEY, ELEVENLABS_VOICE_ID (default: the service's
// default voice) and ELEVENLABS_MODEL (default eleven_v4). stt reads
// HEKATE_MODELS_DIR (default ./data/models) and downloads the model there if
// it is missing.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/voice/elevenlabs"
	"github.com/loafykitten/hekate/service/internal/voice/parakeet"
)

const (
	defaultVoice = "EXAVITQu4vr4xnSDxMaL"
	defaultModel = "eleven_v4"
)

func main() { os.Exit(realMain()) }

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "voicecheck:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string) error {
	switch {
	case len(args) == 3 && args[0] == "tts":
		return tts(ctx, args[1], args[2])
	case len(args) == 2 && args[0] == "stt":
		return stt(ctx, args[1])
	}
	return errors.New(`usage: voicecheck tts "text" out.mp3 | voicecheck stt in.wav`)
}

func tts(ctx context.Context, text, out string) error {
	key := os.Getenv("ELEVENLABS_API_KEY")
	if key == "" {
		return errors.New("ELEVENLABS_API_KEY is not set")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := elevenlabs.New(key, envOr("ELEVENLABS_VOICE_ID", defaultVoice), envOr("ELEVENLABS_MODEL", defaultModel),
		elevenlabs.WithLogger(log))

	start := time.Now()
	sp, err := c.Synthesize(ctx, text)
	if err != nil {
		return err
	}
	took := time.Since(start)
	if err := os.WriteFile(out, sp.Audio, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	a := sp.Alignment
	fmt.Printf("latency   %v\n", took.Round(time.Millisecond))
	fmt.Printf("audio     %d bytes %s, %.2f s -> %s\n", len(sp.Audio), sp.Format, sp.Duration, out)
	fmt.Printf("alignment %d characters, matches input: %v\n", len(a.Chars), strings.Join(a.Chars, "") == text)
	// Tags show up as ordinary characters that span real time; print each one.
	for i := 0; i < len(a.Chars); i++ {
		if a.Chars[i] != "[" {
			continue
		}
		j := i
		for j < len(a.Chars) && a.Chars[j] != "]" {
			j++
		}
		if j == len(a.Chars) {
			break
		}
		fmt.Printf("tag       %-14s %.2f-%.2f s\n", strings.Join(a.Chars[i:j+1], ""), a.Starts[i], a.Ends[j])
		i = j
	}
	return nil
}

func stt(ctx context.Context, wav string) error {
	pcm, rate, err := parakeet.ReadWAV(wav)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	t := parakeet.New(ctx, envOr("HEKATE_MODELS_DIR", "./data/models"), log)
	defer t.Close()

	loadStart := time.Now()
	select {
	case <-t.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	if !t.Ready() {
		return errors.New("speech model did not become ready (see log)")
	}
	fmt.Printf("model     ready in %v\n", time.Since(loadStart).Round(time.Millisecond))

	start := time.Now()
	text, err := t.Transcribe(ctx, pcm, rate)
	if err != nil {
		return err
	}
	fmt.Printf("audio     %.2f s at %d Hz\n", float64(len(pcm))/float64(rate), rate)
	fmt.Printf("latency   %v\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("text      %s\n", text)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
