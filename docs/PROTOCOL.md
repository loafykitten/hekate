# Client ↔ service protocol

The PWA talks to the service over one WebSocket for conversation and plain REST for everything
the menu shows. This file is the contract: change it in the same PR as the code.

## Conversation socket: `GET /api/ws`

JSON text frames, except push-to-talk audio, which is sent as binary frames. Every message has
a `type`. Ids made by the client (`id`) are opaque strings (UUIDs). The server echoes them so
the client can match replies.

All connected clients receive every turn event (phone and desktop stay in sync). Only the
client whose input started a turn (`origin` matches its `client_id`) plays the audio.
`transcript` and errors about a specific input go only to the client that sent it.

### Server → client

| type | fields | meaning |
|---|---|---|
| `ready` | `client_id`, `conversation_id`, `capabilities: { stt, tts }`, `gestures: string[]` | Sent once on connect. `stt`/`tts` are false when unconfigured. |
| `capabilities` | `capabilities: { stt, tts }` | What the service can do changed after connecting (e.g. speech-to-text finished loading). |
| `transcript` | `id`, `text`, `superseded?` | Push-to-talk result for input `id`. An empty `text` means nothing was heard, and no turn follows. `superseded: true` means newer input (from any device) arrived while it was transcribed, so it was dropped. |
| `turn_start` | `turn_id`, `input_id`, `origin`, `user_text` | She's answering `user_text`. |
| `activity` | `turn_id`, `tool`, `label`, `state: "start" \| "done" \| "error"` | A tool is running, e.g. label "Checking your calendar". |
| `segment` | see below | One sentence of her reply, ready to perform. |
| `card` | `turn_id`, `card: { kind, id, title, data }` | Something to show on screen (M2+). |
| `turn_end` | `turn_id`, `status: "done" \| "interrupted" \| "error"`, `text`, `model`, `usage: { input_tokens, output_tokens, cost_usd \| null }` | The turn is over. `text` is the full display text. |
| `error` | `code`, `message`, `input_id?` | A problem the user should see. Codes: `llm_unavailable` (no key), `llm_error`, `empty_reply`, `turn_timeout`, `stt_unavailable` (unconfigured or model still loading), `stt_failed`, `bad_message`, `too_long`. |

`segment`:

```jsonc
{
  "type": "segment",
  "turn_id": "t_…",
  "seq": 0,                         // 0, 1, 2… within the turn, in playback order
  "text": "Morning, Rika.",         // display text: tags removed
  "duration": 1.42,                 // seconds; estimated from reading speed when there's no audio
  "audio_b64": "…",                 // null without TTS
  "audio_format": "mp3",            // null without TTS
  "words": [{ "text": "Morning,", "start": 0.0, "end": 0.41 }],   // null without TTS
  "gestures": [{ "name": "waving", "at": 0.0 }],                   // seconds into this segment
  "expressions": [{ "name": "laughs", "at": 0.9 }]                 // ElevenLabs tags, for faces
}
```

The client plays segments back to back in `seq` order. It reveals `text` word by word using
`words` (or evenly over `duration` without TTS), fires gestures at `at`, and drives the mouth
from the audio's loudness.

### Client → server

| type | fields | meaning |
|---|---|---|
| `say` | `id`, `text` | Typed input. |
| `ptt_start` | `id`, `sample_rate` (16000) | Push-to-talk started. Binary frames follow: little-endian signed 16-bit mono PCM. She stops talking at once on every device (the current turn ends as `interrupted`, keeping what was delivered). |
| `ptt_end` | `id` | Released. The server transcribes, sends `transcript`, then starts a turn. |
| `ptt_cancel` | `id` | Discard the audio. |
| `interrupt` | `heard_seq` (last segment seq the user heard at least partly, or -1), `turn_id` (the turn `heard_seq` describes) | Stop that turn. The server keeps segments up to `heard_seq` in history, and ignores an interrupt for a turn that is no longer the current one. |

An `interrupt` that arrives after the turn already ended (the client was still playing queued audio) trims the saved reply to `heard_seq` and broadcasts a second `turn_end` with status `interrupted`, so every client stops playing that turn. With no recent turn it is ignored. New input while a turn is running interrupts that turn first. The client sends `interrupt` with its `heard_seq` only for a turn it started (it knows what was heard); for another device's turn it just sends the input, and the server interrupts that turn keeping what it delivered.

### Limits

- Text input ≤ 4000 characters. Push-to-talk ≤ 60 s of audio (longer is cut and transcribed).
- One turn at a time per conversation.
- The socket is pinged every 20 s. Clients reconnect with backoff, and on reconnect they get a
  fresh `ready` and refetch history over REST.

## REST

All under `/api/`, JSON, errors as `{ "error": "message" }` with a 4xx/5xx status.

| Method & path | Returns |
|---|---|
| `GET /api/conversation/messages?before=<message id>&limit=<n ≤ 100>` | `{ messages: [{ id, role: "user" \| "assistant", text, turn_id, created_at }] }`, newest last |

Later milestones add tasks, reminders, calendar, mail, memory, approvals, activity, usage and
settings, and document them here.
