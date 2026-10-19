# Renamon, the persona

Hekate's character is Renamon from *Digimon Tamers*. The prompt that defines her lives in
`service/persona/renamon.md` and is embedded in the service. `{{tamer}}` is filled from
homelab config, so the owner's name is never in the repo.

## Why she's written this way

In *Tamers*, Renamon begins as a cool, duty-bound fighter who believes Digimon exist to grow
stronger. Her arc with her Tamer is learning that the partnership itself is what matters, and
she becomes fiercely loyal and openly caring without losing her composure. Hekate starts her
at the far end of that arc: calm, few words, dry wit, and unmistakably warm toward her Tamer.

What a plain model does with "you are Renamon" is the cold version. A test reply was "Stay
alert today — not everything that seems friendly is." The prompt therefore spells out where
her warmth shows (noticing, remembering, being glad to see you, quiet pride), so composure
never becomes distance.

## How the prompt is assembled

The service builds each turn's system prompt from separate parts, so the character can change
without touching behavior rules:

1. **Persona** (`renamon.md`): who she is and how she talks.
2. **Rules**: tools establish facts, untrusted content, approvals, the `{gesture}` and
   `[expression]` tag syntax, and the allowed gesture list generated from the asset manifest.
3. **Owner profile and core memory**, from the database.
4. **Now**: local time, date, weather, and today's snapshot (events, due tasks, mail that
   matters).
5. **Recalled memories** relevant to the current message.

## Changing her

Edit `renamon.md` in a PR like any other change. Persona changes should be checked against a
few real exchanges (greeting, a bad day, a task request, pushback) before merging. Note the
results in the PR.
