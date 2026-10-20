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

All under `/api/`, JSON, errors as `{ "error": "message" }` with a 4xx/5xx status. Request bodies must be `application/json` (415 otherwise), and state-changing requests from another origin are refused with 403 (cross-origin protection via `Sec-Fetch-Site`/`Origin`).

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
| `POST /api/tasks/{id}/complete` | optional `{ due }`: the occurrence being completed | `Task`. A repeating task records the completion and moves `due` to the next occurrence instead of closing. With `due`, a request for an occurrence that's no longer current changes nothing (no double completion). |
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
  "created_by_hekate": true,
  "version": "…"                    // the stored event's version (CalDAV ETag)
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/calendar/status` | | `{ configured, calendar_name? }` |
| `GET /api/calendar/events?from=&to=` (default today through +7 days) | | `{ events: [Event] }` sorted by start |
| `POST /api/calendar/events` | `{ title, start, end?, all_day?, location?, notes?, rrule? }` | `Event` (201) |
| `PATCH /api/calendar/events/{uid}` | Event fields (applies to the whole series). Send the `version` you loaded: an edit against an older version is refused with 409. Send `instance` when editing from one occurrence: a moved `start` shifts the series by the same amount (whole days for all-day events) and a new `end` sets its length. | `Event` |

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

### Mail

Mail accounts come from the homelab's environment (`HEKATE_MAIL_<n>_*`). Hekate keeps a header
cache of each account's inbox with her classification; bodies are fetched from IMAP on demand and
never stored.

```jsonc
// Message
{
  "id": "381",
  "account": "work",                 // the account's label
  "from": { "name": "Shopify", "address": "no-reply@example.com" },
  "subject": "Your weekly update",
  "date": "2026-10-19T08:12:00-05:00",
  "folder": "INBOX",                 // where it is now: INBOX, Archive, Trash or another folder; "other" once it left the inbox for somewhere Hekate doesn't know
  "unread": true,
  "category": "newsletter",          // needs_reply | important | personal | receipt | notification | newsletter | promo, null until classified
  "gist": "Product news and a sale", // her few-word summary, null until classified
  "can_unsubscribe": true            // the sender offers one-click unsubscribe (RFC 8058)
}
// MailRule: applied by the sweep to new inbox mail
{
  "id": "4",
  "match": { "from": "@example.com", "category": null },  // from: an address or "@domain"; every set field must match
  "action": "archive",               // archive | trash | mark_read | move
  "folder": null,                    // for move
  "label": "Archive mail from example.com",
  "hits": 12, "last_hit_at": "…", "created_at": "…"
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/mail/status` | | `{ configured, accounts: [{ name, address, ok, error, last_sweep_at, inbox_count }] }` |
| `GET /api/mail/summary` | | `{ unread: { <category>: n }, attention: [Message] }`: unread inbox mail by category (`unclassified` for mail not classified yet; categories with none are left out), and up to 10 unread `needs_reply`/`important` messages, newest first |
| `GET /api/mail/messages?q=&category=&account=&folder=inbox\|all&unread=&limit=` (default inbox, 50, ≤ 200) | | `{ messages: [Message] }` newest first; `q` searches sender, subject and gist |
| `GET /api/mail/messages/{id}` | | `{ message: Message, body: { text, truncated } }`, fetched live as plain text without marking it read; 404 when it's gone from the server |
| `POST /api/mail/messages/{id}/archive` | | `Message` (its new `folder`; the id stays valid); 404 when it's gone from the server |
| `POST /api/mail/messages/{id}/trash` | | `Message`; 404 when it's gone from the server |
| `POST /api/mail/messages/{id}/read` | `{ unread: false \| true }` (required) | `Message`; 404 when it's gone from the server |
| `GET /api/mail/rules` | | `{ rules: [MailRule] }` |
| `DELETE /api/mail/rules/{id}` | | 204 |

A message keeps its `id` when it moves between folders. Mail that someone else moved or deleted
is marked as having left the inbox (`folder: "other"`) and answers 404 to reads and actions; such
rows are forgotten after 60 days. Server problems answer 502.

The owner's own actions through REST just happen. Renamon's mail tools archive, move and mark
up to 10 messages directly when asked; trashing, unsubscribing, new rules and anything larger go
through an approval. With no accounts configured, `status.configured` is false, the lists are
empty and her tools say mail isn't set up.

### Approvals

Actions that need the owner's say-so wait as approvals. Allowing one runs the action on the
service and records what happened; "Always" also stores a standing rule for that action and
scope, so the next matching request runs without asking (such a run appears as an approval already `done`/`failed` with `decided_by: "rule"`). Pending approvals expire after 24 h (a change request after 7 days) and become `expired` (`decided_by` stays null). Each approval is decided once: a second decision, from another device or a double tap, gets 409 and runs nothing. An allowed action reads `running` until it finishes, then `done` or `failed` (the `approval` event carries each change); one cut off by a restart becomes `failed` saying so.

```jsonc
// Approval
{
  "id": "5",
  "kind": "mail_trash",              // the action
  "title": "Move 14 newsletters from example.com to Trash?",
  "detail": "",                      // optional plain text
  "scope_label": "mail from example.com",   // what "Always" covers; null when it can't become a rule
  "status": "pending",               // pending | running | done | failed | denied | expired
  "result": null,                    // after running: "Moved 14 messages to Trash" or the error
  "decided_by": null,                // owner | rule
  "note": null,                      // the owner's reason for a denial, if they gave one
  "created_at": "…", "expires_at": "…", "decided_at": null
}
// ApprovalRule
{ "id": "2", "kind": "mail_trash", "scope_label": "mail from example.com", "uses": 3, "created_at": "…" }
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/approvals?status=pending\|recent` (default pending) | | `{ approvals: [Approval] }`; `recent` is everything decided, run by a rule or expired in the last 7 days, newest first |
| `POST /api/approvals/{id}/decide` | `{ decision: "once" \| "always" \| "deny", note? }` | `Approval` after the action ran; 409 when it's no longer pending; "always" on an approval without a scope is 400. `note` (up to 500 characters) goes with `deny` only: Renamon reads it |
| `GET /api/approvals/rules` | | `{ rules: [ApprovalRule] }` |
| `DELETE /api/approvals/rules/{id}` | | 204 |

### Change requests

Renamon proposes changes to Hekate itself with her `propose_change` tool (at most 5 in 24 h).
Each proposal waits as an approval of kind `change_request`. Allowing it puts the change on the
list (`queued`, shown as "Approved") for the owner and Claude Code to build by hand, through the
usual PR gate. Whoever builds it records the progress with `claim`, `progress` and `finish`, so
the owner and Renamon can follow it. The owner can put a failed or rejected change back on the
list. Denying a proposal can carry a note; she reads it and may propose a revised version, which
names the one it `revises`. There is no auth beyond the tailnet and the device allowlist, like
the rest of the API.

```jsonc
// ChangeRequest
{
  "id": "3",
  "title": "Match mail rules on subject words",
  "what": "…",                         // what to build, in her words
  "why": "…",                          // why it helps
  "kind": "feature",                   // feature | fix | improvement
  "revises": null,                     // id of the denied proposal this replaces
  "status": "queued",                  // proposed | denied | expired | queued | working | merged | rejected | failed
  "approval_id": "41",
  "feedback": null,                    // the owner's note when denied
  "stage": "",                         // the latest step while it's built, e.g. "Building", "PR open, in review"
  "issue_url": null,                   // https://github.com/… once filed
  "pr_url": null,
  "result": null,                      // once finished: what happened, in a line or two
  "attempts": 1,                       // how many times it has been taken up
  "previous_result": null,             // after a retry: how the last attempt ended
  "previous_pr_url": null,             // and its PR
  "created_at": "…", "updated_at": "…", "finished_at": null
}
```

| Method & path | Body | Returns |
|---|---|---|
| `GET /api/changes?status=` (optional, one status) | | `{ changes: [ChangeRequest] }`, newest first, last 60 days |
| `GET /api/changes/{id}` | | `ChangeRequest` |
| `POST /api/changes/{id}/claim` | | `ChangeRequest` now `working`; 409 unless it was `queued` |
| `POST /api/changes/{id}/progress` | `{ stage, issue_url?, pr_url? }` | `ChangeRequest`; 409 unless `working`. URLs must be `https://github.com/…` |
| `POST /api/changes/{id}/retry` | | `ChangeRequest` queued again, keeping its issue; the last outcome and PR move to `previous_*`. 409 unless `failed` or `rejected`. The owner's action |
| `POST /api/changes/{id}/finish` | `{ outcome: "merged" \| "rejected" \| "failed", result, issue_url?, pr_url? }` | `ChangeRequest`; 409 unless `working`. Tells the owner by notify and push |

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
| `mail` | `{ items: [Message], highlight?: [id] }` |
| `mail_message` | `{ message: Message, excerpt }` (the start of the body she read) |
| `approval` | `Approval`. The card shows Allow once / Always / Deny while pending, and the outcome after. |

## More server → client events

| type | fields | meaning |
|---|---|---|
| `notify` | `id`, `kind: "reminder" \| "approval" \| "change"`, `title`, `body`, `card?` | Something fired, or she needs a decision she asked for outside a conversation (e.g. a rule from the weekly noise report). The app shows it even with no turn running. |
| `approval` | `approval: Approval` | An approval was created or changed (decided, ran, expired). Clients update any card showing it. |
| `invalidate` | `domains: ("tasks" \| "reminders" \| "calendar" \| "memory" \| "weather" \| "mail" \| "approvals" \| "changes")[]` | Data changed; refetch those menu sections (`weather`: the home place changed). |
