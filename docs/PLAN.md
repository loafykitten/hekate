# Hekate plan

Hekate is a personal assistant for one person, with Renamon as its face. A service runs 24/7 on
a homelab and a web app (PWA) reached over Tailscale renders Renamon full screen. You talk to her
(push-to-talk or text), she answers in her own voice with gestures, and she shows what she found
as cards. Behind that conversation she manages your calendar, tasks, reminders, lists and email
noise, remembers what matters, and reflects on her own day.

It's for daily life, not software work: casual conversation, keeping the day in order and
quietly taking chores off your plate.

`docs/HANDOFF.md` covers the character slice (what exists in `app/`, the asset pipeline and
licensing). `docs/DEVELOPMENT.md` covers how AI agents work on this repo: gates, review and
deploys. `docs/PERSONA.md` explains who Renamon is here.

## Principles

- **One owner.** No accounts, tenants or onboarding. Auth is the tailnet plus an allowlist of
  the owner's devices at the reverse proxy.
- **Tools establish facts.** She never says something is done unless a tool result says so,
  and cards are filled from the database by id, never from model text.
- **Quiet by default.** Proactive features notify only when something crosses a threshold. A
  silent heartbeat counts as a successful one.
- **Ask before it hurts.** Reads and the owner's direct requests just happen. Destructive or
  outward actions, such as deleting mail, unsubscribing or touching events she didn't create,
  ask first, and the owner can turn an approval into a standing rule.
- **Public code, private life.** The repo is public. Secrets, hostnames, addresses, personal
  profile and character assets live only on the homelab, and examples use placeholders.
- **No mocks.** Each feature is built for real when its turn comes. An unconfigured integration
  (no ElevenLabs key, no mail password) is reported as unavailable, never faked.

## Architecture

```
 phone / desktop (PWA)                         homelab (Docker, inside the Tailscale sidecar netns)
 ┌──────────────────────┐   HTTPS + WSS        ┌─────────────┐
 │ three.js Renamon     │ ───────────────────▶ │ Caddy       │  hekate.<domain>  → 127.0.0.1:3200
 │ React overlay: PTT,  │   (tailnet only,     │ (existing)  │  dav.<domain>     → 127.0.0.1:5232
 │ text, cards, menu    │    device allowlist) └──────┬──────┘
 │ service worker: push │                             │
 └──────────────────────┘                      ┌──────▼─────────────────────────────────────────┐
 iPhone/Mac/Android calendar apps ─ CalDAV ───▶│ hekate (Go)                                    │
                                               │  agent loop · tools · scheduler · web push     │
                                               │  STT (Parakeet, in-process) · TTS (ElevenLabs) │
                                               │  SQLite (+FTS5) · serves the built PWA + assets│
                                               └──┬──────────┬──────────┬──────────┬────────────┘
                                                  │          │          │          │
                                           Neuralwatt   Zoho IMAP   Radicale   Brave Search,
                                           (GLM 5.3)    (2 accts)   (CalDAV)   Open-Meteo
```

### Service: Go

Go over Rust: the mail and calendar protocol libraries decide it. `emersion/go-imap` v2 and
`emersion/go-webdav` (CalDAV) are the best maintained of either ecosystem. Go also compiles in
seconds, which keeps AI edit-test loops tight, and the throughput needed (one user) is trivial
for either. Quality comes from strict linting, tests and the review gate, not the language.

- Go 1.25+, standard `net/http` routing, `log/slog` (JSON) to stdout and a rotating file.
- SQLite via `modernc.org/sqlite` (pure Go, FTS5 built in), embedded forward-only migrations,
  `VACUUM INTO` backup before any migration and nightly.
- STT through `k2-fsa/sherpa-onnx-go` (cgo) running NVIDIA Parakeet TDT 0.6B v2 int8 (English).
  The model is downloaded on first start into a volume and checked against a pinned SHA-256.
- One binary serves the API, WebSocket, the built web app and the character assets (mounted
  from the host, never in the image).

### Web app: React PWA (`app/`)

- React 19 + TypeScript + Vite, `vite-plugin-pwa` for the manifest and service worker,
  TanStack Query for menu data, CSS modules with design tokens. React is the framework AI
  models write most reliably, and the existing three.js `Character` stays framework-free.
- Full-screen Renamon (existing renderer), push-to-talk button (hold, or tap to toggle), a
  keyboard toggle for text entry, speech bubble, cards that slide in beside her, and a menu
  sheet with Today, Tasks & Lists, Reminders, Calendar, Mail, Memory and Hekate (status, logs,
  usage, ideas).
- Installable on iOS/Android/desktop, with web push for reminders and check-ins. iOS needs the
  app added to the home screen for push.

### Calendar: Radicale

Radicale is the owner's single calendar, hosted on the homelab. iPhone, Mac and Android (DAVx5)
add it as a CalDAV account, and Hekate reads and writes it as a CalDAV client. One store for
everything avoids splitting events across the two mail providers' calendars. It's reachable
only on the tailnet and uses htpasswd auth.

### Inference: Neuralwatt GLM 5.3

| Use | Model | Effort | Why |
|---|---|---|---|
| Conversation (default) | `glm-5.3-flash` | low | ~0.8 s to first word, a tenth of the price |
| Conversation needing depth | `glm-5.3` | high | Flash calls `think_deeper` and the turn continues on the full model |
| Reflection, rule learning | `glm-5.3-flex` | high | Not latency sensitive; flex tier is cheaper |
| Bulk mail classification | `glm-5.3-flash-flex` | low | Many small calls |

Measured 2026-10-19: both models stream first content in 0.3–0.8 s. Reasoning is mandatory
and defaults to `max`, so every request sets `reasoning_effort` explicitly. Flex requires
`stream: true`. Reasoning text is never shown or spoken.

## The agent

### A turn

1. Input arrives (text, or a PTT transcript) on the conversation WebSocket.
2. Prompt = persona + owner profile + core memories + time, date and location + today's
   snapshot (next events, due tasks, unread important mail count) + recent conversation +
   relevant recalled memories (FTS) + tool schemas.
3. The model streams. Visible text is split into sentences, and each sentence goes to TTS and
   then to the client as a *segment* with audio, word timings and gesture events.
4. Tool calls run with bounds (6 rounds, 12 calls, 90 s turn deadline). Reads run in parallel
   and writes are ordered. Each call and its result is recorded in the activity log.
5. Tools that look up or change something the owner cares about (tasks, events, weather…)
   attach a card built from the stored data, and the client shows it beside her.

Interrupting her (PTT while she talks) stops playback, cancels the rest of the turn and keeps
what was already said in history.

### Tools

| Area | Tools | Effect |
|---|---|---|
| Memory | `memory_search`, `memory_save`, `memory_update`, `memory_forget` | local |
| Tasks & lists | `task_list`, `task_create`, `task_update`, `task_complete` (one-off and RRULE repeating; named lists like groceries) | local |
| Reminders | `reminder_list`, `reminder_create`, `reminder_update`, `reminder_cancel` (one-off and repeating; delivered by push and in app) | local |
| Calendar | `calendar_events`, `event_create`, `event_update`, `event_delete` | CalDAV; deleting events she didn't create asks first |
| Mail | `mail_search`, `mail_read`, `mail_summary`, `mail_move`, `mail_archive`, `mail_mark`, `mail_trash`, `mail_unsubscribe`, `mail_rule_propose` | IMAP; trash, unsubscribe and bulk actions ask first |
| Web | `web_search` (Brave), `web_read` (fetch + readability) | read |
| World | `weather` (Open-Meteo), `time_in` | read |
| Self | `self_status`, `self_logs`, `self_usage`, `self_activity`, `idea_log` | read / local |
| Reasoning | `think_deeper` (escalate to the full model) | none |

Tools are registered per feature module with a JSON schema, an effect class, a timeout and
an output cap. Results from mail and web pages are untrusted data, and the prompt says so.
Nothing in an email can authorize an action.

### Approvals

An action that needs approval becomes an approval card ("Move 14 newsletters from Shopify to
Trash?") with **Allow once**, **Always allow this** (stored as a rule scoped to the action and
its target, e.g. this sender), and **Deny**. Pending approvals expire after 24 h. Rules can be
reviewed and revoked in the menu.

### Memory and reflection

- **Core memory**: a short, always-loaded profile (name, people, preferences, routines). She
  can propose changes, and each change is logged.
- **Memories**: atomic notes (`fact`, `preference`, `person`, `event`, `follow_up`) with
  importance, source turn ids, optional `follow_up_at`, and FTS5 search. She saves them during
  conversation when something durable comes up.
- **Diary**: one entry per day, written by reflection.
- **Daily reflection** (03:30, `glm-5.3-flex`): reads the day's conversations, actions and
  current memories, then saves memories it missed, merges duplicates, retires stale or
  contradicted ones, schedules follow-ups ("ask how the interview went") and writes the diary
  entry. Every change is recorded in a reflection report visible in the menu, and anything can
  be undone.
- Saying "forget that" deletes the memory and its derived copies. The conversation archive
  stays unless the owner deletes it.

### Proactive behavior

- **Morning briefing**, started by the owner: the day's first "good morning" gets a spoken
  catch-up with cards (weather, what's coming up, due tasks, reminders, mail that matters). Nothing
  is pushed ahead of time, since the owner's day starts at different hours.
- **Heartbeat** (every 30 min, waking hours only): cheap checks for an event starting soon
  with no prep, an overdue task, important unread mail or a due follow-up. It notifies only on
  a threshold and never repeats a nudge.
- **Mail sweep** (every 10 min): classifies new mail (needs reply / important / personal /
  receipt / notification / newsletter / promo), applies the owner's standing rules, and keeps
  noise stats. A weekly **noise report** proposes new rules ("You never open these 3 senders.
  Archive them automatically?").
- **Evening check-in** (optional): a light "how did today go?" that feeds the diary.

### Self-inspection

She can read her own structured logs, job runs, deploy history (the deployer writes a log the
service mounts read-only), token usage and cost, and her action audit trail. When asked "is
something wrong?" she checks those before answering. `idea_log` records improvement ideas she
has, such as a tool she wished she had or a recurring failure, and they're listed in the menu.

## Voice and performance

As agreed in `HANDOFF.md`: `{gesture}` tags (allowed names come from the manifest) and
ElevenLabs `[expression]` tags in the model's text. The service strips them for display, calls
ElevenLabs *with timestamps* per sentence, and maps gesture offsets to times. The client
receives `{ seq, text, audio, words[{text,start,end}], events[{gesture,at}], expressions }` per
segment, plays segments back to back, fires gestures on time and reveals words in sync.

PTT: the client captures 16 kHz mono PCM through an AudioWorklet and streams it over the
WebSocket. On release the service transcribes with Parakeet (~0.5–1.5 s on the homelab CPU)
and echoes the transcript before the reply starts.

Without an ElevenLabs key she answers in the bubble only, revealed at reading speed. Settings
show TTS as "not configured".

## Data model (SQLite)

`conversations`, `messages` (role, text, tool calls, model, usage), `memories` + `memories_fts`,
`core_memory`, `diary`, `reflections`, `tasks` (title, notes, list, due, rrule, priority,
completed_at), `task_completions`, `reminders` (text, fire_at, rrule, delivered/snoozed state),
`mail_accounts`, `mail_messages` (header cache + classification + FTS), `mail_rules`,
`approvals`, `approval_rules`, `push_subscriptions`, `jobs` + `job_runs`, `activity` (tool audit),
`usage` (per request: model, tokens, cost), `ideas`, `settings`.

Calendar events stay in Radicale (the source of truth phones sync with). Hekate caches nothing
but etags.

## Protocol (summary)

- `GET /healthz` → `{ ok, version, commit, uptime }`.
- `GET /character/*` serves the mounted asset folder (manifest + GLBs only).
- REST under `/api/`: today, tasks, lists, reminders, calendar, mail, memory, approvals,
  activity, usage, ideas, settings, push subscribe.
- `GET /api/ws`: one conversation socket. Client → server: `say`, `ptt_start` + binary PCM
  frames + `ptt_end`, `interrupt` (approvals are decided over REST). Server → client:
  `transcript`, `turn_start`, `segment` (+ binary audio frame), `card`, `activity` ("checking
  your mail…"), `approval`, `turn_end`, `notify`, `invalidate` (menu data changed), `error`.

Full message shapes live in `docs/PROTOCOL.md` alongside the code that implements them.

## Security and privacy

- Caddy allowlists the owner's tailnet device IPs (TCP peer, not forwarded headers), and the
  service listens only on loopback inside the sidecar namespace.
- Secrets live only in `hekate.env` on the homelab (mode 600), never in the image or repo.
  CI runs gitleaks on every PR.
- Mail bodies are fetched on demand and not stored. Header cache and classifications are kept
  for search and stats.
- Prompt-injection posture: content from mail and the web is wrapped and labeled as untrusted,
  and external-effect tools need the owner's request or a standing rule.
- Logs never contain secrets or full mail bodies.

## Delivery

Every milestone ships through the PR gate in `DEVELOPMENT.md` and deploys automatically on
merge.

Status (2026-10-20): M0–M5 are merged and running on the homelab, plus tap-to-talk, a desktop
mic that opens with the app, 24 character clips with idle variations, change requests, the
Telegram backup channel, a voice-off toggle and a morning briefing the owner starts.

| Milestone | Contents | Done when |
|---|---|---|
| **M0 Foundation** ✅ | Repo, CI, gates, image build, homelab deployer, Caddy route, Radicale, service skeleton serving the PWA shell | `hekate.<domain>` loads Renamon from the homelab over Tailscale; merging a PR redeploys it |
| **M1 Conversation** ✅ | Neuralwatt client, agent loop, persona, history, WebSocket, text chat, PTT + Parakeet STT, ElevenLabs TTS + segments + gestures | Talk to her by voice from the phone and she answers in voice with gestures |
| **M2 Daily life** ✅ | Tasks, lists, reminders + web push, memory, calendar (CalDAV), web search/read, weather, cards, menu | Ask her to add things, remind you, schedule; see it all in the menu and on the phone calendar |
| **M3 Mail** ✅ | Two Zoho accounts over IMAP, sweep + classification, rules, approvals, noise report | She tells you what matters and cleans what you've agreed to |
| **M4 Proactive** ✅ | Briefing, heartbeat, follow-ups, reflection + diary, self-inspection, ideas | She greets you with your day and reflects nightly |
| **M5 Polish** ✅ | Viseme lip sync from TTS timings, expression map, offline shell, UX passes | Her mouth makes the shapes of her words, the app opens offline, every screen reviewed |

### M5 as built (2026-10-20)

- **Viseme lip sync**: the service turns the voice's per-character timing into timed mouth shapes
  (`perform/visemes.go`; viseme classes, English letter rules); the app maps them to the rig's
  morphs from the manifest and scales them by loudness (`character/lips.ts`). Lines without
  usable timing, and a voice that's off, fall back to the loudness-driven mouth.
  `npm run verify:lipsync` checks it on the real character.
- **Offline shell**: after one online visit the app opens with the service out of reach: the shell
  and the character come from the service worker's cache (a first visit asks for the character
  again once the worker controls the page, so it is cached), the pill says Offline, and the menu
  shows the last-known data with an "Offline · as of <time>" note in the header. The service
  worker still never caches `/api/*`. The menu's read queries are kept in localStorage by TanStack
  Query's persister (`app/src/data/persist.ts`): successful reads of the menu domains only, for up
  to a week from when they were fetched, under a buster that is a hash of `app/src/data/` (a
  schema change drops the old copy). Nothing is stored while a change is in flight, so an
  unconfirmed one is never kept. Never stored: a single mail message with its body, single
  approvals, searches, the health poll, the conversation socket. Storage that is blocked or full
  just means no cache. Actions that need the service say "Hekate isn't reachable."
  `npm run verify:offline` covers it.
- **Expression map**: ElevenLabs tags already drive her face since M1 (`[laughs]` squints and
  smiles, `[sighs]` lowers the brows and lids; `app/src/conversation/schedule.ts`).
- **UX pass**: `npm run verify:tour` screenshots every screen on phone and desktop. The review
  fixed the service-down state (one "Hekate isn't reachable." toast instead of one raw proxy
  error per section, and today's events say they couldn't load instead of spinning), the task
  field's placeholder, and the calendar's day strip, which now fades at its edges.

### M4 as built (2026-10-20)

- **Routines** run on a small scheduler (`service/internal/jobs`): daily at a time, caught up
  after an outage, or every interval. Every run is recorded and visible to her (`self_activity`).
- **Self-inspection** (`service/internal/inspect`): `self_status`, `self_logs`, `self_usage`
  (conversation by model, background work by source), `self_activity` (tool calls, routine runs,
  deploys), and `idea_log`; her ideas are listed under Changes.
- **Reflection** (`service/internal/reflection`), nightly at 03:30: tidies memories and writes the
  diary. Changes apply only to memories as she read them, each is recorded in the same transaction
  and can be undone from the Diary; merges are linked so forgetting either side forgets both.
- **Heartbeat** (`service/internal/routines`), every 30 minutes in waking hours: one notice about
  what's new (an event to leave for, overdue tasks, mail that needs the owner, due follow-ups),
  never repeating a nudge. Settings → Routines, or `routine_settings`.
- **Morning briefing**: first built as a scheduled write-up, pushed and performed when the app
  opened. Changed on 2026-10-20 at the owner's request, because the day doesn't start at a fixed
  hour and a pushed briefing is easy to miss or swipe away: now the day's first message containing
  "good morning" opens its turn with `morning_briefing`, gathered fresh (the next 24 hours, not the
  calendar day), and she answers with the catch-up. The day counts once that reply is given in
  full, so a failed or interrupted one is tried again on the next good morning. She can also call
  the tool whenever asked.
- Not built: the optional evening check-in.

### Next: change requests, Telegram, voice off

**Change requests.** Renamon gets a `propose_change` tool (title, what, why, kind). It's told to
keep proposals to what's relevant: no personal context, nothing remotely NSFW, nothing that
identifies the owner beyond their name and pronouns. Proposals are stored in Hekate (not sent
to GitHub) and capped at about five a day.

1. The owner approves or denies each proposal on an approval card. A denial can carry feedback;
   Renamon reads it and either lets the idea go or proposes a revised version.
2. Approved proposals wait in the Changes menu. The owner and Claude Code build them by hand,
   through the usual PR gate, recording progress so the owner and Renamon can follow it.

Built 2026-10-19 (`service/internal/changes`). Proposals expire after 7 days unanswered, and
denials carry an optional reason for every approval kind, not just change requests. An automated
worker that built and merged approved changes unattended ran for one evening and was removed the
same day: the owner prefers to build them together.

**Telegram.** Notifications and actions only, not a second chat. Hekate stays the first channel;
Telegram is the backup:

- A notification web push can't get to any device (none subscribed, or every send failed) goes
  to the owner's chat instead: reminders, approvals asked outside a conversation, finished
  changes. Approvals come with Allow and Deny buttons; a reply to one denies it with the reply as
  the reason. The service long-polls the Bot API, so nothing is exposed, and only the owner's
  chat is answered (`service/internal/telegram`).
- A failed deploy (rolled back or not) is sent by the deployer itself, which works while Hekate
  is down.
- Outages: the deployer pings a healthchecks.io check each minute while Hekate is healthy; when
  the pings stop, healthchecks.io's own Telegram integration tells the owner.

Built 2026-10-19 (setup in `deploy/README.md`).

**Voice off.** A speaker toggle in the dock and in Settings, remembered per device. Off means
replies arrive as text at reading speed with gestures and expressions but no audio, and the
service skips speech synthesis for those turns.

## What we took from OpenClaw, Muse and Grok Bot

Researched 2026-10-19.

- **OpenClaw**: persona/profile/memory files → core memory + persona; *heartbeat* with a
  checklist and quiet-unless-needed → heartbeat; cron with one-shot/interval jobs → scheduler;
  tiered memory (always-loaded / daily / searchable) → core memory / diary / memories; "email
  steward" (archive noise, label, alert) → mail sweep; health check that notifies only when
  broken → self-inspection; learning loop (corrections → rules) → approval rules and reflection.
- **Meta Muse**: remembers details mentioned once and acts on them later → memories with
  `follow_up_at`; goal plans with progress → possible later milestone; read-only vs act on mail
  → effect classes; asks before sensitive actions with an audit trail → approvals + activity.
- **Grok Bot**: picks up dropped threads → follow-ups; allow once / always allow / deny, with
  require-approval winning over always-allow → approval rules; routines run on a schedule or an
  event and stay quiet when there's nothing to do → jobs; an inbox routine that skips
  newsletters and builds an attention list → mail sweep; stall reporting → she says when a tool
  keeps failing instead of looping.

Skipped on purpose: browser automation and purchasing (risk without much benefit for daily
life), multi-bot fleets, third-party skill marketplaces (untrusted code), messaging-app
channels (the PWA is the channel; Telegram could be added later as another client of the same
agent).

Sources: [OpenClaw config](https://github.com/TechNickAI/openclaw-config),
[OpenClaw overview](https://agentic-ai.readthedocs.io/en/latest/AgentPlatforms/openclaw/),
[Meta Muse](https://www.datacamp.com/blog/muse-agent),
[Introducing Grok Bot](https://x.ai/news/introducing-grok-bot),
[Grok Bot deep dive](https://flaviocopes.com/grok-bot/).
