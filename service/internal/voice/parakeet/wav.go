package parakeet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// ReadWAV reads a mono 16-bit PCM WAV file. It exists for the voicecheck dev
// tool and tests; the service itself receives raw PCM over the WebSocket.
func ReadWAV(path string) (pcm []int16, sampleRate int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read wav: %w", err)
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a RIFF/WAVE file")
	}
	le := binary.LittleEndian
	haveFmt := false
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(le.Uint32(b[off+4:off+8]))
		body := off + 8
		// Streamed WAVs may claim a huge data size; clamp to what is there.
		end := min(body+size, len(b))
		switch id {
		case "fmt ":
			if end-body < 16 {
				return nil, 0, errors.New("short fmt chunk")
			}
			format, channels, bits := le.Uint16(b[body:]), le.Uint16(b[body+2:]), le.Uint16(b[body+14:])
			if format != 1 || channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("need mono 16-bit PCM, got format %d, %d channels, %d bits", format, channels, bits)
			}
			sampleRate = int(le.Uint32(b[body+4:]))
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, 0, errors.New("data chunk before fmt chunk")
			}
			pcm = make([]int16, (end-body)/2)
			for i := range pcm {
				pcm[i] = int16(le.Uint16(b[body+2*i:])) //nolint:gosec // G115: the same 16 bits read as signed
			}
			return pcm, sampleRate, nil
		}
		off = body + size + size%2
	}
	return nil, 0, errors.New("no data chunk")
}
