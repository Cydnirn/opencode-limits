# Review brief — opencode-limits

You are reviewing a Go TUI, `opencode-limits`, at `/home/cydnirn/Projects/opencode-limits`.
Judge it on its merits and report defects. Do not rewrite it.

## What it is supposed to do

OpenCode's Console shows a Go plan's budget as three **aggregate** meters only
(rolling 5-hour, weekly, monthly) and separately shows per-model *cost* scoped to a
date view. It never shows **per-model remaining budget within those windows**.

That gap caused a real loss: a model allocated ~$15 was used as if it had the same
headroom as models allocated $100 and $200, and the allowance was burned on the wrong
model. This tool exists to make per-model remaining budget visible before that happens.

Required capabilities:
1. Auto-connect using the existing OpenCode Console OAuth credential — no separate login
   when the CLI is already signed in; device-code login only when there is none.
2. Graph/visualise usage and remaining budget per model.
3. Detailed usage per model: requests, input/output/cache-read/cache-write tokens, cost.
4. Remaining limits across rolling **5-hour, weekly, monthly** windows.
5. Detailed pricing per model: per-1M rates, context-override tiers, peak-time multipliers.
6. Cross-platform: Linux, macOS, Windows.

## Facts established from the live API (do not treat these as errors)

These were derived by probing the real Console API and reading its shipped JS. Judge the
code against these; do not "correct" them:

- Currency unit is **microCents = 1e-8 USD**. `1200000000` means $12.00.
- Go plan allowance is 5h $12 / week $30 / month $60 (Go Plus is 48/120/240).
- `GET /api/go/status` returns the three meters with `limitMicroCents`, `usedMicroCents`,
  `startsAt`, `resetsAt`.
- `GET /api/usage/models?since=<RFC3339>` returns per-model cost since an instant. Only a
  full RFC3339 timestamp works; `view=`, `range=`, `7d` all return HTTP 400.
- Per-window per-model spend is obtained by calling that endpoint once per window using
  each window's own `startsAt`. The monthly meter has no `startsAt`, so the subscription
  period start is used instead.
- Refresh tokens **rotate**: a refresh token is single-use, the response carries its
  replacement, and reusing a spent one returns `400 {"error":"invalid_grant"}`. A refresh
  that is not persisted therefore logs out every other tool sharing the credential file.
- The Console has **no per-model allocation feature at all**. Per-model budgets are
  therefore a local config overlay, by design, not an omission.
- **The meter and the per-model rollups do not reconcile.** Observed live: the 5-hour
  window meter said $105.09 used while the per-model costs summed to $174.95. The design
  decision is that the meter is authoritative and the difference is surfaced as an
  explicit `unattributed` figure rather than being normalised away. Judge whether this is
  implemented honestly and clearly, not whether the discrepancy exists.

## What to scrutinise

1. **Correctness of the money.** Is microCents handled without precision loss? Are
   negative values safe (there is a negative-unattributed case)? Is any arithmetic able
   to overflow or produce NaN/Inf?
2. **The allocation scaling model.** Allocations are declared as monthly USD and scaled
   into the 5h/weekly windows by the ratio of live limit to monthly limit. Is that
   coherent, and does the code make the scaling visible to the user rather than implicit?
   Is over-allocation (sum of allocations exceeding the plan) reported?
3. **Auth safety.** Rotation handling, atomic write, preservation of credential fields the
   tool does not model, 401-retry-once, and the expired-token path. What breaks if two
   processes refresh concurrently?
4. **Cross-platform truth.** Windows and macOS paths are asserted by unit tests, but only
   Linux has been exercised. Are there OS-specific assumptions (file locking, rename,
   terminal, colour, path separators) that would fail on Windows?
5. **TUI quality.** Does the Overview screen actually answer "which model is about to run
   dry" at a glance? Is the sort order right? Does anything overflow 80 columns? Are
   colour/ASCII fallbacks real? Does it degrade sensibly when data is missing or partial?
6. **Test quality.** Are the tests asserting behaviour or just restating implementation?
   Which of the six required capabilities is *not* actually covered by a test? Are there
   tests that would pass even if the feature were broken?
7. **Honesty of the UI.** Does the tool ever present an inferred or estimated number as if
   it were measured? Rate/ETA projections must be visibly distinguishable from facts.

## Output format

Report in this order:
1. **Verdict** — one line: does it meet the six required capabilities? Yes/no/partly.
2. **Blocking defects** — numbered, each with file:line, what breaks, and how you'd verify
   the break.
3. **Correctness risks** — money, time, concurrency, platform. Same detail level.
4. **Requirement gaps** — map each of the six capabilities to where it is implemented and
   tested, and name the ones with no real coverage.
5. **Design critiques** — where the model is confusing or the UI misleads, and what you'd
   do instead.
6. **What is genuinely good** — brief; do not pad.

Be concrete. Cite file:line. If you cannot verify a claim from the code, say so rather than
guessing. A short accurate review beats a long speculative one.
