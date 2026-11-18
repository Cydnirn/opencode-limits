# opencode-limits

A TUI for OpenCode Go plans that shows **per-model spending and remaining budget**
across the rolling 5-hour, weekly and monthly windows — the thing the Console's
budgets page does not show.

The Console reports Go usage as three aggregate meters (`5-hour 25%`,
`Weekly 55%`, `Monthly 27%`) and, separately, a per-model cost table scoped to a
date range. Nothing connects the two, and there is no per-model budget anywhere
in the Console. So an expensive model quietly eats an allowance you meant for a
cheap one and nothing warns you.

`opencode-limits` closes that gap: you declare how you want to divide the plan
per model, and it shows each model's spend against that share, sorted so the
model closest to running dry is at the top.

## Install

Requires Go 1.24 or newer. Nothing else — the build is pure Go with
`CGO_ENABLED=0`, so the result is a single static binary.

### Linux and macOS

```bash
git clone https://github.com/cydnirn/opencode-limits
cd opencode-limits
make install PREFIX=$HOME/.local     # no sudo needed
```

`make install` puts the binary in `$PREFIX/bin` and tells you if that directory
is not on your `PATH`. `make` on its own builds to `./bin`.

### Windows

From Git Bash, MSYS2 or WSL (any shell with GNU make — `winget install
GnuWin32.Make` or `choco install make` if you need it):

```bash
make install
```

On Windows the Makefile installs to `$(go env GOPATH)/bin`, which is normally
already on `PATH` for Go users. To pick another location:

```bash
make install PREFIX="C:/Tools"
```

### Without make

Any platform, one command:

```bash
go build -o opencode-limits ./cmd/opencode-limits
```

### Prebuilt binaries

Download from the [Releases page](https://github.com/Cydnirn/opencode-limits/releases):

```bash
# Linux / macOS — pick your platform from the release, then:
chmod +x opencode-limits_v0.1.0_linux_amd64
./opencode-limits_v0.1.0_linux_amd64
```

| OS | Architectures |
|---|---|
| Linux | amd64, arm64 |
| macOS | amd64 (Intel), arm64 (Apple silicon) |
| Windows | amd64, arm64 |

Each binary ships with a `.sha256` beside it:

```bash
sha256sum -c opencode-limits_v0.1.0_linux_amd64.sha256
```

They are static (`CGO_ENABLED=0`), so there is no runtime to install.

### Build it yourself

`make release` cross-compiles every supported target into `./dist`.

## First run

The tool uses the OpenCode Console credential already on your machine, written by
the `opencode-console` CLI. If `opencode` is signed in, this just works — no
separate login.

```bash
opencode-limits            # the TUI
opencode-limits --once     # plain-text report, for scripts or a quick check
```

If no credential is found it runs the same device-code login the CLI uses and
prints a code and URL. To force a fresh sign-in: `opencode-limits --login`.

### Per-model limits come from the plan

You do **not** declare budgets any more. The plan itself publishes a monthly
limit per model, and the tool uses it. Inspect the table at any time:

```bash
opencode-limits --allowances          # the full per-model table
opencode-limits --check-allowances    # diff the embedded copy against the live one
```

The figures match the published table at
[opencode.ai/console/go](https://opencode.ai/console/go) — for Go:

| Model | Monthly cap |
|---|---|
| GPT-6 Luna, Kimi K3, DeepSeek V4 Pro, Claude Haiku 5.5 | $15 |
| Qwen3.8 Flash | $30 |
| Gemini 4 Argon | $30 |
| GLM-5.3-Flash, MiniMax-M3, Kimi K2.7 Code | $60 |
| DeepSeek V4.1 Flash | $100 |
| GLM-5.3 | $500 |
| Beam Free and other free models | unlimited |

Go Plus values are roughly double; `--allowances` prints all three columns.

**These are independent ceilings, not shares of a pool.** Each model can spend up
to its own monthly cap; using $15 of GPT-6 Luna does not reduce what GLM-5.3 may
spend, and the caps are never summed into one budget. That is why the Overview
shows "remaining" per model against its own cap rather than a total.

Where the numbers come from: OpenCode does not expose them over its API — the
model catalog at `/api/v1/models` and the models.dev feed both omit the field. Only
the public web bundle carries them, as `go.monthly_usage` per model. The tool ships
an embedded copy (so it works offline) and refreshes it from that bundle when the
network allows, falling back to the embedded figures on failure.

### Optional: local overrides

A model the plan does not publish a cap for can still get one, via `config.toml`:

```toml
[allocations]
"some-unpublished-model" = 40   # monthly USD
```

A published cap always wins over a local override.

## Screens

| Key | Screen | What it shows |
|---|---|---|
| `1` | Overview | Three window gauges, then per-model budget sorted by remaining (least first) |
| `2` | Models | Per-model tokens (input, output, cache-read, cache-write), cost, effective $/1M, monthly remaining |
| `3` | Pricing | Per-1M rates, context-override tiers, peak-time multipliers, context/output limits |
| `4` | Trend | Daily spend chart and per-model request share |

### Keys

| Key | Action |
|---|---|
| `1`–`4`, `tab` / `shift+tab` | switch screen |
| `↑` / `↓`, `k` / `j` | scroll the current screen |
| `pgup` / `pgdn` | scroll one page |
| `home` / `end`, `g` / `G` | jump to top / bottom |
| `s` | cycle Models sort order (remaining, spend, requests, tokens) |
| `r` | refresh now (also refreshes every 60s) |
| `?` | help |
| `q` / `esc` | quit |

The tab bar shows `↕ 12/39` when a screen has more content than fits, so you can
tell there is more below the fold.

## Reading the numbers

- **The meter is the truth.** The plan's own meter is authoritative. Per-model
  costs come from a separate usage endpoint and their sum does not match the
  meter: rollups lag and the meter counts differently. Observed live, the
  5-hour meter read $105.09 while per-model costs summed to $174.95. Rather than
  fudging it, the difference is shown as an explicit **unattributed** line.
  A large unattributed figure means a lot of usage cannot be attributed to a
  model by the API.
- **`~` means projected.** Anything with `~` (the `RUNS OUT~` column) is
  extrapolated from burn rate so far in the window. It is not reported by the
  API. Measured and inferred values are kept visibly separate.
- **A model with `--` has no allocation.** Its plan share is still shown; only
  the allocation columns are blank.

## Development

```bash
make help        # every target
make             # build for this machine into ./bin
make test        # go vet + race tests
make test-cover  # coverage summary
make lint        # fail on unformatted code or vet errors
make release     # six cross-compiled binaries in ./dist
make clean
```

`make test` and `make release` are also the two commands CI should run.

## How it authenticates

It reads and writes the credential the `opencode-console` CLI owns. Refresh
tokens **rotate** — each one is single-use — so a refresh persists the new token
atomically while preserving the fields this tool does not model.

If two processes refresh at once, the loser gets `invalid_grant`. That does not
mean the session is dead, so the tool reloads the file and adopts the winner's
token. Only if the file still holds the rejected token does it report a problem,
and it will not start a new login in that state because that would overwrite a
credential that is probably still valid.
