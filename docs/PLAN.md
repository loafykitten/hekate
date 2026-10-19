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
- Installable on iOS/Android/desktop, with web push for reminders and briefings. iOS needs the
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
| Reflection, briefing, rule learning | `glm-5.3-flex` | high | Not latency sensitive; flex tier is cheaper |
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

- **Morning briefing** (configurable time): weather, today's events, due tasks, reminders and
  mail that matters. Sent as a push notification; opening the app gets a spoken greeting with
  cards.
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
  frames + `ptt_end`, `interrupt`, `approve`. Server → client: `transcript`, `turn_start`,
  `segment` (+ binary audio frame), `card`, `activity` ("checking your mail…"), `approval`,
  `turn_end`, `notify`, `invalidate` (menu data changed), `error`.

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

| Milestone | Contents | Done when |
|---|---|---|
| **M0 Foundation** | Repo, CI, gates, image build, homelab deployer, Caddy route, Radicale, service skeleton serving the PWA shell | `hekate.<domain>` loads Renamon from the homelab over Tailscale; merging a PR redeploys it |
| **M1 Conversation** | Neuralwatt client, agent loop, persona, history, WebSocket, text chat, PTT + Parakeet STT, ElevenLabs TTS + segments + gestures | Talk to her by voice from the phone and she answers in voice with gestures |
| **M2 Daily life** | Tasks, lists, reminders + web push, memory, calendar (CalDAV), web search/read, weather, cards, menu | Ask her to add things, remind you, schedule; see it all in the menu and on the phone calendar |
| **M3 Mail** | Two Zoho accounts over IMAP, sweep + classification, rules, approvals, noise report | She tells you what matters and cleans what you've agreed to |
| **M4 Proactive** | Briefing, heartbeat, follow-ups, reflection + diary, self-inspection, ideas | She greets you with your day and reflects nightly |
| **M5 Polish** | Viseme lip sync from TTS timings, expression map, offline shell, UX passes | — |

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
