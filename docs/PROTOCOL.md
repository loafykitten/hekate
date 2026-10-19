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
| `ready` | `client_id`, `conversation_id`, `capabilities: { stt, tts }`, `gestures: string[]`, `timezone` | Sent once on connect. `stt`/`tts` are false when unconfigured. `timezone` is the IANA zone the service schedules in; clients display times in it. |
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


Times are RFC 3339 with offset. "Local" means the configured `HEKATE_TIMEZONE`. A date-only value
is `YYYY-MM-DD`. Every change made through REST or by Renamon's tools broadcasts an `invalidate`
event (below) so open menus refresh.

### Tasks and lists

```jsonc
// Task
{
  "id": "12",
  "title": "Oat milk",
  "notes": "",
  "list": "Groceries",          // "Inbox" when not given
  "due": "2026-10-20",          // null, a date (all-day) or an RFC 3339 time
  "rrule": null,                // RFC 5545 RRULE for repeating tasks, e.g. "FREQ=WEEKLY;BYDAY=MO"
  "priority": 0,                // 0 none, 1 low, 2 medium, 3 high
  "completed_at": null,
  "created_at": "…", "updated_at": "…"
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/tasks?list=&status=open\|done\|all` (default open) | | `{ tasks: [Task] }` ordered by due (nulls last), then priority |
| `POST /api/tasks` | `{ title, notes?, list?, due?, rrule?, priority? }` | `Task` (201) |
| `PATCH /api/tasks/{id}` | any Task fields above | `Task` |
| `POST /api/tasks/{id}/complete` | | `Task`. A repeating task records the completion and moves `due` to the next occurrence instead of closing. |
| `POST /api/tasks/{id}/reopen` | | `Task` |
| `DELETE /api/tasks/{id}` | | 204 |
| `GET /api/lists` | | `{ lists: [{ name, open_count }] }` |

### Reminders

```jsonc
// Reminder
{
  "id": "7",
  "text": "Call the dentist",
  "at": "2026-10-20T09:00:00-05:00",   // next time it fires
  "rrule": null,
  "status": "scheduled",               // scheduled | fired | done | cancelled
  "last_fired_at": null,
  "created_at": "…"
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/reminders?status=upcoming\|past\|all` (default upcoming) | | `{ reminders: [Reminder] }` |
| `POST /api/reminders` | `{ text, at, rrule? }` | `Reminder` (201) |
| `PATCH /api/reminders/{id}` | `{ text?, at?, rrule? }` | `Reminder` |
| `POST /api/reminders/{id}/snooze` | `{ minutes }` | `Reminder` |
| `POST /api/reminders/{id}/done` | | `Reminder` |
| `DELETE /api/reminders/{id}` | | 204 (cancels) |

When a reminder fires, the service sends web push to every subscription and a `notify` event
to connected clients. A repeating reminder is rescheduled to its next occurrence.

### Push

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/push/key` | | `{ public_key }` (VAPID, base64url) |
| `POST /api/push/subscriptions` | the browser's `PushSubscription.toJSON()` | 204 |
| `DELETE /api/push/subscriptions` | `{ endpoint }` | 204 |
| `POST /api/push/test` | | 204 after sending a test notification; 409 when no device is subscribed; 502 when every push service rejected it |

Push payload (JSON): `{ title, body, url, tag }`. The service worker shows it, and a click
focuses or opens `url`.

### Calendar

```jsonc
// Event (recurrences are expanded into instances inside the requested range)
{
  "uid": "…",                       // stable across instances of a recurring event
  "instance": "2026-10-20T15:00:00-05:00",   // null for a non-recurring event
  "title": "Dentist",
  "start": "2026-10-20T15:00:00-05:00",      // a date for all-day events
  "end": "2026-10-20T16:00:00-05:00",
  "all_day": false,
  "location": "",
  "notes": "",
  "rrule": null,
  "created_by_hekate": true
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/calendar/status` | | `{ configured, calendar_name? }` |
| `GET /api/calendar/events?from=&to=` (default today through +7 days) | | `{ events: [Event] }` sorted by start |
| `POST /api/calendar/events` | `{ title, start, end?, all_day?, location?, notes?, rrule? }` | `Event` (201) |
| `PATCH /api/calendar/events/{uid}` | Event fields (applies to the whole series) | `Event` |

All-day events use an exclusive end date (RFC 5545): a one-day event on the 20th has `start: "2026-10-20"`, `end: "2026-10-21"`. Renamon can only edit or delete events she created; the owner can change any event through REST.
| `DELETE /api/calendar/events/{uid}` | | 204 (whole series) |

### Memory

```jsonc
// Memory
{ "id": "3", "kind": "preference", "content": "Takes coffee black", "importance": 3,
  "follow_up_at": null, "created_at": "…", "updated_at": "…" }
// kind: fact | preference | person | event | follow_up; importance 1–5
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/memory?q=&kind=&limit=` | | `{ memories: [Memory] }`, full-text ranked when `q` is set, else newest first |
| `PATCH /api/memory/{id}` | `{ content?, kind?, importance?, follow_up_at? }` | `Memory` |
| `DELETE /api/memory/{id}` | | 204 |
| `GET /api/memory/core` | | `{ entries: [{ key, value, updated_at }] }` (the always-loaded profile) |
| `PUT /api/memory/core/{key}` | `{ value }` | the entry |
| `DELETE /api/memory/core/{key}` | | 204 |

### Weather

| Method & path | Returns |
|---|---|
| `GET /api/weather?place=` (default: the home place from settings) | `Weather` card data (below), or 404 `{ error }` when no place is known |

## Cards

`card.card` is `{ kind, id, title, data }`. Tools attach cards to their results; cards are built
from stored or fetched data, never from model text. Shapes of `data` by `kind`:

| kind | data |
|---|---|
| `tasks` | `{ items: [Task], highlight?: [id] }`. One list, or the tasks just changed. |
| `reminders` | `{ items: [Reminder], highlight?: [id] }` |
| `events` | `{ from, to, items: [Event], highlight?: [uid] }` |
| `memory` | `{ items: [Memory] }` (what she saved or recalled) |
| `weather` | `{ place, unit: "F" \| "C", now: { temp, feels_like, condition, code }, today: { high, low, precip_chance, sunrise, sunset }, hours: [{ time, temp, precip_chance, code }] (next 12), days: [{ date, high, low, precip_chance, code }] (next 5) }` (`code` is the WMO weather code) |
| `web_results` | `{ query, items: [{ title, url, snippet, source }] }` |
| `web_page` | `{ title, url, source, excerpt }` |

## More server → client events

| type | fields | meaning |
|---|---|---|
| `notify` | `id`, `kind: "reminder"`, `title`, `body`, `card?` | Something fired. The app shows it even with no turn running. |
| `invalidate` | `domains: ("tasks" \| "reminders" \| "calendar" \| "memory" \| "weather")[]` | Data changed; refetch those menu sections (`weather`: the home place changed). |
