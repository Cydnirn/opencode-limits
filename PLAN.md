# opencode-limits — Plan of Record

A TUI that complements the opencode TUI by showing **per-model spend and remaining
budget** across rolling 5-hour, weekly, and monthly windows — the thing the Console's
budgets page does not show.

## 1. The problem, precisely

The Console shows Go usage as **three aggregate meters** (`5-hour 8%`, `Weekly 55%`,
`Monthly 27%`) and, separately, a per-model cost table scoped to a *view*, not a
budget window. Nothing connects the two. There is no per-model allocation anywhere
in the Console product — confirmed by reading every Console route chunk: the only
"allocation" symbol is a billing ledger entity unrelated to model budgets.

So `$15 on GPT Sol` vs `$200 on GLM` exists only in the operator's head. That is why
burning the cheap-model allowance on an expensive model is invisible until the
aggregate meter moves.

**This tool's job:** make per-model budget state first-class, by letting the operator
declare per-model allocations and then overlaying actual per-model spend against
them, in the same windows the meters use.

## 2. Data sources (verified live, 2026-11-18)

Auth: `~/.local/share/opencode-console/credential.json`, written by the
`opencode-console` CLI. Shape:

```json
{"type":"oauth","server":"https://opencode.ai/console","access":"...","refresh":"...",
 "expires":1797444750156,"accountID":"user_...","email":"...",
 "orgID":"org_...","orgName":"Personal"}
```

Every call sends `Authorization: Bearer <access>` + `x-org-id: <orgID>`.

| Endpoint | Gives | Notes |
|---|---|---|
| `GET /api/go/status` | 5h / week / month meters: `limitMicroCents`, `usedMicroCents`, `startsAt`, `resetsAt`; plan `product`; subscription window | The authoritative budget truth |
| `GET /api/go/subscription/models` | Enabled model list + display names | Which models the plan allows |
| `GET /api/usage/models?since=<ISO8601>&pageSize=N` | Per-model requests/tokens/**cost** since a timestamp | `since` must be ISO-8601; `view=`, `range=`, `7d` all 400 |
| `GET /api/v1/models` | Model catalog: `limit.{context,input,output}`, `cost.text.{input,cache,output}` incl. context overrides & multipliers | Static pricing, no auth-quota cost |
| `GET /api/billing/drawer/status` | Balance, credit, billing mode | Extra-usage context |
| `GET /api/activity/go` | Per-day totals + per-model request counts | Trend bars |
| `GET /api/usage/summary` | Workspace totals | Header line |

**Unit model (important):** `microCents` is **1e-8 USD**. `1200000000` = $12.00.
Go = $10/mo → 5h limit $12, weekly $30, monthly $60. These match the Console's own
plan table (`fiveHoursFromFirstUseMicroCents: 1200000000n`, etc.), so the tool derives
limits from a built-in plan table as a fallback and always prefers the live values.

**Per-window per-model spend** is derived by calling `/api/usage/models?since=` with
each window's own `startsAt`. This is the crux: three calls, one per window, yields
per-model cost per window. Verified: 5h window returned only the one model active
since that window opened.

**Known discrepancy, handled explicitly:** summing per-model `cost` from
`/api/usage/models` under-reports the meter (5h: $174.95 summed vs $105.09 meter;
week: $749 summed vs $1669.66 meter). Rollups lag and the meter appears to count
differently. The tool therefore **never sums models to infer the meter**. It shows
meter truth from `/api/go/status` and model attribution from `/api/usage/models`,
and reports the unattributed remainder as an explicit `unattributed` row so the
numbers reconcile visibly instead of silently lying.

## 3. Per-model allocations

The Console has no concept of them, so they live in a local config file:

`<config>/opencode-limits/config.toml` (or `allocations.json`)

```toml
[allocations]
"gpt-6-luna" = 15.0
"deepseek-v4.1-flash" = 100.0
"glm-5.3" = 200.0
```

Semantics: an allocation is a **share of the plan's window budget** allocated to that
model, expressed in USD per **monthly** period, scaled proportionally into 5h/week.
If allocations sum to more than the plan allowance, the tool flags
`over-allocated` rather than silently normalising.

Remaining per model = `allocation_scaled_to_window − spend_in_window`.

## 4. Architecture

Single static Go binary, no runtime deps, cross-platform (linux/darwin/windows ×
amd64/arm64).

```
opencode-limits/
├── cmd/opencode-limits/main.go      # flag parsing, wiring
├── internal/
│   ├── console/                     # API client + auth + token refresh
│   │   ├── client.go
│   │   ├── auth.go                  # credential discovery per-OS, rotation-safe
│   │   ├── types.go
│   │   └── mock.go                  # fixture server for tests
│   ├── budget/                      # allocations, window scaling, reconciliation
│   │   ├── allocate.go
│   │   └── window.go
│   ├── tui/                         # bubbletea model, views, charts
│   │   ├── app.go
│   │   ├── overview.go
│   │   ├── models.go
│   │   ├── pricing.go
│   │   ├── sparkline.go
│   │   └── styles.go
│   └── config/                      # TOML/JSON config + XDG/AppData paths
└── docs/
```

**Why Go + bubbletea:** the tool must ship as a single binary on three OSes with no
Node/Python runtime; the opencode CLI itself is a Bun single binary and users expect
that. Go cross-compiles trivially (`GOOS=windows go build`), bubbletea is the
maintained TUI stack (v1.3.10), and ntcharts gives braille line charts without a
graphics dependency. Alternative considered: Rust + ratatui — equally fine, but slower
to iterate and the HTTP/JSON story is heavier. Python/Textual — rejected, requires an
interpreter on Windows.

**Auth auto-connect:** read the existing `opencode-console` credential file (already
present because opencode is signed in). On expiry, POST `/auth/device/token` with
`grant_type=refresh_token`, `client_id=opencode-cli`. **Refresh tokens rotate** —
verified: a second refresh with a spent token returns `invalid_grant`. So on refresh
the new access+refresh+expires are written back atomically (temp file + rename),
preserving the rest of the file, or login breaks for every other tool sharing it.
If no credential exists, run the device-code flow (`POST /auth/device/code`,
poll `POST /auth/device/token`) and print a code + URL.

Per-OS credential location, mirroring the CLI's own resolution
(`XDG_DATA_HOME || ~/.local/share` on Linux, `~/Library/Application Support` on
macOS, `%LOCALAPPDATA%` on Windows — plus `OPENCODE_CONSOLE_SERVER` override).

## 5. Screens

1. **Overview** — the money screen. Three window gauges (5h/weekly/monthly) with
   percent, used/limit, reset countdown; then a per-model table sorted by
   *remaining budget ascending* so the model about to run dry is at the top, with a
   bar per model showing spent vs allocated and a burn-rate ETA ("runs out in 3h").
   Unattributed remainder row pinned at the bottom.
2. **Models** — full per-model detail: requests, input/output/cache-read/cache-write
   tokens, cost per window, allocation, remaining, % of plan budget consumed,
   effective $/1M tokens, context/output limits.
3. **Pricing** — the catalog: input, cache read, cache write, output price per 1M,
   context-override tiers, time multipliers (DeepSeek's weekday peak ×2), context and
   output token limits. Sorted to make "cheap vs expensive" obvious.
4. **Trend** — daily cost bars from `/api/activity/go` + per-model request split.

Keyboard: `1..4`/`tab` screens, `r` refresh, `w` cycle window focus, `?` help, `q` quit.
Auto-refresh on a ticker (60s, matching the Console's own refetch cadence).

## 6. Cross-platform requirements

- Paths via a single `paths.go` with `runtime.GOOS` switches; no shelling out.
- No `sqlite3`/CGO — pure Go only, so `CGO_ENABLED=0` builds run everywhere.
- Atomic credential writes work on Windows (rename-over requires removing target first;
  handle `EEXIST`/sharing violation).
- Colour: honour `NO_COLOR`, degrade to ASCII if the terminal reports no UTF-8.
- Release matrix: linux/darwin/windows × amd64/arm64 via a build script + GoReleaser-style
  `Makefile` target.

## 7. Testing

- `console` against a fixture HTTP server replaying captured payloads (the real ones in
  this plan) — table tests for unit conversion, window scaling, `since` construction.
- `budget` pure-unit tests: allocation scaling, over-allocation, remaining math,
  unattributed reconciliation.
- Auth tests: refresh rotation write-back, expired-token path, missing credential.
- TUI: golden/testable render via `tea.Model.Update` on a fixed terminal size.
- Live smoke: `go run ./cmd/opencode-limits --once` prints a text report from the real API.

## 8. Non-goals

- Not a replacement for the opencode TUI; it does not launch or drive sessions.
- No write access to Console (no changing plan, no buying credits).
- No local session-DB parsing — the server is authoritative, so third-party
  machines and other agents' usage are included for free.

## 9. Install and self-update (v0.1.2)

Two halves, because the two audiences are different.

**`install.sh`** is for a machine with no Go toolchain: resolve the newest release,
download the asset for the host, verify it against the published `.sha256`, run it
once to prove it starts, install atomically. POSIX sh so it runs under dash as
`curl … | sh`, and the `latest` tag comes from the release redirect rather than
the JSON API — no token, no rate limit.

**`--update`** is for the installed binary: it knows its own build-time version,
compares it with the newest release, verifies and renames over itself. Doing this
in Go rather than shelling back out to `install.sh` is what makes it work on
Windows, where there is no shell to rely on.

### Decisions worth recording

| Question | Decision | Why |
|---|---|---|
| Where does the version come from? | `-ldflags` stamp, never the file on disk | Nothing records what was installed; comparing bytes would make every local build look stale |
| What about `git describe` suffixes? | Treated as the tag, not a pre-release | Semver would sort `v0.1.2-5-gabc` **below** `v0.1.2`, so `--update` would offer a downgrade |
| Replace a running binary how? | Temp file in the target dir, then `rename` | Writing in place fails with `ETXTBSY`; a cross-filesystem rename is not atomic |
| Windows? | Leave `<exe>.new`, print the `move /Y` command, exit 0 | Windows cannot rename over a running executable. Nothing applies it automatically at launch — the user runs the printed command after closing the tool |
| Install directory? | `$BINDIR` → `$PREFIX/bin` → `/usr/local/bin` if writable → `~/.local/bin` | Never require sudo when a user-writable option exists |
| Not on `PATH`? | Print the exact line for bash/zsh/fish | Installing somewhere unreachable is a silent failure |
| Read-only target? | Report path, keep the verified binary, print the install command | Failing quietly on `sudo`-owned dirs is the common real case |

Tags: v0.1.1 carries the catalog-decode fix; v0.1.2 adds the update flags and the
installer; v0.1.3 makes `go install` builds self-updatable. v0.1.1 had to be
superseded because the docs describe `--update`, and a binary without it would have
shipped documented-but-absent flags.

### Where the version comes from, in full

Three sources, in priority order:

1. `-ldflags -X main.version` — `make build`, the release workflow. Authoritative.
2. `debug.ReadBuildInfo()`'s module version — fills in for `go install`, which
   cannot pass linker flags and would otherwise report `dev` forever.
3. `dev` — nothing usable. Refuses to self-update.

Step 2 only accepts a version fetched from the module proxy, which carries the tag
verbatim. A `go build` in a working tree embeds a PSEUDO-version
(`v0.1.3-0.20261119…-d41`), which names commits *after* a tag; the comparator ranks
it below the release it descends from, so accepting it would turn `--update` into a
downgrade. Those builds stay `dev`. Guarded by `releaseVersionFromModule` and its
8-case table.
