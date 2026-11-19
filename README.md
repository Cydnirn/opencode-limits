# opencode-limits

A TUI for OpenCode Go plans that shows **per-model spending and remaining budget**
across the rolling 5-hour, weekly and monthly windows.

The Console reports Go usage as three aggregate meters (`5-hour 25%`,
`Weekly 55%`, `Monthly 27%`) and, separately, a per-model cost table scoped to a
date range. Nothing connects the two, and there is no per-model budget anywhere
in the Console. So an expensive model quietly eats an allowance you meant for a
cheap one and nothing warns you.

In `opencode-limits` you declare how you want to divide the plan
per model, and it shows each model's spend against that share, sorted so the
model closest to running dry at the top.

## Install

Only the source builds need a toolchain, and then only Go 1.24 or newer. The
release binaries and the installer need neither Go nor a compiler. The build is
pure Go with `CGO_ENABLED=0`, so the result is a single static binary.

### Linux and macOS: one-line installer

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh
```

The script resolves the newest release for your OS and architecture, downloads
it, checks it against the published `.sha256`, runs it once to confirm it
starts, then installs it. It needs `curl` and `sha256sum` (or `shasum`) — no Go,
git or python.

The install directory is the first of these that applies:

1. `$BINDIR`
2. `$PREFIX/bin`
3. `/usr/local/bin`, if it exists and you can write to it
4. `$HOME/.local/bin`

If the chosen directory is not on `PATH`, the installer prints the exact line to
add for bash, zsh or fish. Progress goes to stderr and the installed path alone
goes to stdout, so `$(...)` captures just the path.

`--dry-run` does everything except write outside a temporary directory, and
`--uninstall` removes the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh -s -- --dry-run
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh -s -- --uninstall
```

`VERSION` pins a release, `BINDIR` and `PREFIX` choose the directory. Windows is
refused by the script — use the `.exe` below or `go install`.

### Manual binary download

Download from the [Releases page](https://github.com/Cydnirn/opencode-limits/releases):

```bash
# Linux / macOS — pick your platform from the release, then:
chmod +x opencode-limits_v0.1.1_linux_amd64
./opencode-limits_v0.1.1_linux_amd64
```

| OS | Architectures | Asset suffix |
|---|---|---|
| Linux | amd64, arm64 | `_linux_amd64`, `_linux_arm64` |
| macOS | amd64 (Intel), arm64 (Apple silicon) | `_darwin_amd64`, `_darwin_arm64` |
| Windows | amd64, arm64 | `_windows_amd64.exe`, `_windows_arm64.exe` |

Each binary ships with a `.sha256` beside it:

```bash
sha256sum -c opencode-limits_v0.1.1_linux_amd64.sha256
```

They are static (`CGO_ENABLED=0`), so there is no runtime to install.

### go install

```bash
go install github.com/cydnirn/opencode-limits/cmd/opencode-limits@latest
```

The module path is lowercase `cydnirn`; `github.com/Cydnirn/...` is rejected by
the toolchain. This puts the binary in `$(go env GOPATH)/bin` — on macOS and
Linux that is normally `$HOME/go/bin`, on Windows `%USERPROFILE%\go\bin`, which
Go's own installer adds to `PATH`. A binary installed this way does not have a
linker-stamped version, but it reads the module version the Go toolchain embedded
and self-updates from it like any other release build (see
[Updating](#updating)). A binary built from a working-tree checkout has no release
version and reports `dev`, which refuses to update.

### Build from source

```bash
git clone https://github.com/cydnirn/opencode-limits
cd opencode-limits
make install PREFIX=$HOME/.local     # no sudo needed
```

`make install` puts the binary in `$PREFIX/bin` and tells you if that directory
is not on your `PATH`. `make` on its own builds to `./bin`.

On Windows, from Git Bash, MSYS2 or WSL (any shell with GNU make — `winget
install GnuWin32.Make` or `choco install make` if you need it), `make install`
writes to `$(go env GOPATH)/bin`. To pick another location:

```bash
make install PREFIX="C:/Tools"
```

Without make, on any platform:

```bash
go build -o opencode-limits ./cmd/opencode-limits
```

`make release` cross-compiles every supported target into `./dist`.

Full per-OS detail — where each platform installs, how `PATH` is handled, and
what to do when it goes wrong — is in [docs/INSTALL.md](docs/INSTALL.md).

### Updating

```bash
opencode-limits --check-update    # asks GitHub, changes nothing
opencode-limits --update          # downloads, verifies, replaces the binary
```

`--check-update` writes nothing and is meant for scripts, so its exit status is
part of the interface:

| Exit | Meaning |
|---|---|
| `0` | Already on the newest release |
| `9` | A newer release exists |
| `1` | The check itself failed (network, rate limit, unreadable version) |

```bash
if opencode-limits --check-update >/dev/null 2>&1; then
  echo "up to date"
elif [ $? -eq 9 ]; then
  echo "update available"
else
  echo "check failed"
fi
```

`--update` re-checks, verifies the download against the release's `.sha256`, and
installs it. Nothing is written if the checksum does not match. It replaces the
running executable through a rename, so it works while the old copy runs; an
already-running instance keeps the old build until restarted.

On Windows a running `.exe` cannot be overwritten, so `--update` writes
`opencode-limits.new.exe` beside it and prints the `move` command to finish.
Builds with no stamped version (`dev`) refuse to update — there is no release to
compare them against.

The released v0.1.1 binaries predate these two flags; a build from source
(`make build`) has them. `opencode-limits --help` lists what any given binary
supports.

## First run

The tool uses the OpenCode Console credential already on your machine, written by
the `opencode-console` CLI. If `opencode` is signed in, this just works.

It reads that file and refreshes it in place rather than keeping a second copy,
so there is one session to sign in to and one to revoke, not two. The credential
grants read access to your Console account — that is what lets the tool report
usage, spend and plan limits — so it is held to the same standard as the CLI's
own: the file stays `0600` and the tool never sends it anywhere but the Console
API. Per-OS locations are in [docs/INSTALL.md](docs/INSTALL.md#credential-and-config-locations).

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

- The plan's own meter is authoritative. Per-model
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
