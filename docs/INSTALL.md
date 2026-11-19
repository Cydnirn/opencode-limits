# Installing opencode-limits

Reference detail for every supported platform. The README has the short version;
this page covers where each OS puts the binary, how `PATH` is handled, where the
credential lives, and what to do when an install goes wrong.

`opencode-limits` is a single static binary (`CGO_ENABLED=0`) for Linux, macOS
and Windows on amd64 and arm64. Nothing else needs to be installed to run it.

## Which method to use

| Method | Needs Go | Needs curl | Platforms | Self-updates |
|---|---|---|---|---|
| One-line installer | no | yes | Linux, macOS | yes |
| Manual binary download | no | yes | Linux, macOS, Windows | yes |
| `go install` | yes | no | all | no (`dev` build) |
| `make install` / `go build` | yes | no | all | no (`dev` build) |

"Self-updates" means the binary carries a release version stamp, which is what
`--check-update` and `--update` compare against. Binaries built from a working
tree report `dev` and refuse to update themselves — see [Updating](#updating).

## Linux and macOS: one-line installer

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh
```

The script detects `linux`/`darwin` and `amd64`/`arm64` from `uname`, resolves the
newest release tag, downloads the matching asset and its `.sha256`, verifies the
digest, executes the binary once with `--version` to prove it runs on this host,
and only then moves it into place. A failure at any step leaves nothing
installed.

Requirements: a POSIX shell, `curl`, and either `sha256sum` or `shasum`.

### Install directory

The first rule that applies wins:

| Order | Source | Result |
|---|---|---|
| 1 | `$BINDIR` | `$BINDIR/opencode-limits` |
| 2 | `$PREFIX` | `$PREFIX/bin/opencode-limits` |
| 3 | default, if `/usr/local/bin` exists and is writable | `/usr/local/bin/opencode-limits` |
| 4 | fallback | `$HOME/.local/bin/opencode-limits` |

`/usr/local/bin` is usually root-owned, so in practice an unprivileged install
lands in `$HOME/.local/bin`. To choose explicitly:

```bash
# a specific directory
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh \
  | BINDIR="$HOME/bin" sh

# a conventional prefix
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh \
  | PREFIX="$HOME/.local" sh
```

The environment goes on the right of the pipe because `sh` inherits it from the
pipeline, not from a subshell on the left.

### PATH

If the install directory is not already on `PATH`, the installer prints the line
for the shell named in `$SHELL`:

| Shell | Line printed |
|---|---|
| bash | `export PATH="$DIR:$PATH"` appended to `~/.bashrc` |
| zsh | `export PATH="$DIR:$PATH"` appended to `~/.zshrc` |
| fish | `fish_add_path DIR` |
| anything else | the `export` line, with a note to put it in the right startup file |

Until the shell is restarted it prints the full path to run instead. Note that
`~/.bashrc` is not read by non-interactive shells, and on macOS the default
shell is zsh — if `opencode-limits: command not found` persists after editing
`~/.bashrc`, that is why.

## Windows

`install.sh` refuses to run on Windows (it detects `MINGW*`, `MSYS*`, `CYGWIN*`
and `Windows_NT`) and prints the Releases URL. This is deliberate: the release
publishes `.exe` files, not the extensionless asset names the script composes,
and the script's `chmod`/rename install sequence does not apply.

Pick one of these instead.

### Download the .exe

From the [Releases page](https://github.com/Cydnirn/opencode-limits/releases),
take the asset for your architecture:

| Architecture | Asset |
|---|---|
| 64-bit Intel/AMD | `opencode-limits_v0.1.1_windows_amd64.exe` |
| Windows on ARM | `opencode-limits_v0.1.1_windows_arm64.exe` |

Rename it to `opencode-limits.exe` and put it somewhere on `PATH`. To verify
before running, in PowerShell:

```powershell
(Get-FileHash .\opencode-limits_v0.1.1_windows_amd64.exe -Algorithm SHA256).Hash.ToLower()
Get-Content .\opencode-limits_v0.1.1_windows_amd64.exe.sha256
```

The two digests must match.

### go install

```bash
go install github.com/cydnirn/opencode-limits/cmd/opencode-limits@latest
```

This writes to `$(go env GOPATH)\bin`, which defaults to
`%USERPROFILE%\go\bin` — not `%LOCALAPPDATA%`. Check it with:

```powershell
go env GOPATH
```

The Go installer adds that `bin` directory to the user `PATH` for you. If it did
not, add it — in `cmd.exe`:

```bat
setx PATH "%PATH%;%USERPROFILE%\go\bin"
```

or in PowerShell:

```powershell
$env:Path += ";$env:USERPROFILE\go\bin"
[Environment]::SetEnvironmentVariable("Path", $env:Path, "User")
```

`setx` truncates at 1024 characters and writes to the user environment only;
neither form affects the shell you already have open. Open a new terminal to
pick the change up.

### Build with make

From Git Bash, MSYS2 or WSL, with GNU make available (`winget install
GnuWin32.Make` or `choco install make`), `make install` writes to
`$(go env GOPATH)/bin` on Windows. `make install PREFIX="C:/Tools"` writes to
`C:/Tools/bin`.

## Credential and config locations

The tool does not create an account or a second credential. It reads and
refreshes the file the `opencode-console` CLI owns, resolved per OS in
`internal/config/paths.go`:

| OS | Credential file |
|---|---|
| Linux | `$XDG_DATA_HOME/opencode-console/credential.json`, else `~/.local/share/opencode-console/credential.json` |
| macOS | `~/Library/Application Support/opencode-console/credential.json` |
| Windows | `%LOCALAPPDATA%\opencode-console\credential.json`, else `%USERPROFILE%\AppData\Local\opencode-console\credential.json` |

`internal/config/paths.go` also resolves this tool's own config and cache:

| OS | `config.toml` | Cache |
|---|---|---|
| Linux | `$XDG_CONFIG_HOME/opencode-limits/config.toml`, else `~/.config/opencode-limits/config.toml` | `$XDG_CACHE_HOME/opencode-limits`, else `~/.cache/opencode-limits` |
| macOS | `~/Library/Application Support/opencode-limits/config.toml` | `~/Library/Caches/opencode-limits` |
| Windows | `%LOCALAPPDATA%\opencode-limits\config.toml` | `%LOCALAPPDATA%\opencode-limits\Cache` |

Because the credential is shared, both tools see the same session: signing in or
out with `opencode` changes what this tool can read, and there is nothing
separate to revoke. `OPENCODE_CONSOLE_SERVER` overrides the Console API origin if
you point the CLI at a different one.

The file is written with mode `0600` on Unix. On Windows it inherits the ACL of
`%LOCALAPPDATA%`, which is already user-scoped.

If no credential exists, the tool starts the same device-code login the CLI uses
and writes the result in the CLI's format, so `opencode` picks it up too.

## Options for install.sh

| Flag / variable | Effect |
|---|---|
| `--dry-run` | Resolve version and asset, download and verify, report the destination. Writes nothing outside a temp directory. |
| `--uninstall` | Remove the binary from the resolved install directory. Exits 0 if it was not there. |
| `--help` | Print usage and exit 0. |
| `VERSION=v0.1.1` | Pin a release instead of resolving the newest. Must start with `v`. |
| `BINDIR=/path` | Install directory. Beats `PREFIX`. |
| `PREFIX=/path` | Install into `/path/bin`. Beats the defaults. |

Progress goes to stderr; only the final installed path goes to stdout. So this
works:

```bash
dir=$(curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh)
```

Flags require `sh -s --` when piped, because the pipe occupies `sh`'s own
argument list:

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh | sh -s -- --dry-run
```

With the script saved to disk, pass flags directly: `sh install.sh --dry-run`.

Pin a version when you need reproducibility:

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh \
  | VERSION=v0.1.1 sh
```

## Verifying the download

Every release asset has a `.sha256` sibling containing `sha256sum` output.

```bash
curl -fsSLO https://github.com/Cydnirn/opencode-limits/releases/download/v0.1.1/opencode-limits_v0.1.1_linux_amd64
curl -fsSLO https://github.com/Cydnirn/opencode-limits/releases/download/v0.1.1/opencode-limits_v0.1.1_linux_amd64.sha256
sha256sum -c opencode-limits_v0.1.1_linux_amd64.sha256
```

Expected output:

```
opencode-limits_v0.1.1_linux_amd64: OK
```

On macOS use `shasum -a 256 -c <file>`. Then confirm it runs:

```bash
chmod +x opencode-limits_v0.1.1_linux_amd64
./opencode-limits_v0.1.1_linux_amd64 --version
```

## Updating

| Command | Exit 0 | Exit 9 | Exit 1 |
|---|---|---|---|
| `--check-update` | already current | newer release exists | the check failed |
| `--update` | current, or successfully replaced | — | download, checksum or write failed |

Exit 9 exists so a wrapper can separate "an update is available" from "something
broke" without parsing output:

```bash
if opencode-limits --check-update >/dev/null 2>&1; then
  echo "up to date"
elif [ $? -eq 9 ]; then
  echo "update available"
else
  echo "check failed"
fi
```

`--update` downloads the asset for the host platform, compares it against the
published digest, and renames it over the running executable. The rename is
atomic within the directory and works while the old inode executes, so no
restart of the updating process is needed — but any *other* running instance
keeps the old build until it is restarted.

On Windows the running `.exe` is locked. `--update` writes
`opencode-limits.new.exe` next to it, prints the `move` command, and exits 0;
close the tool and run that command, or delete the `.new` file to abandon it.

Both commands refuse on a `dev` build, because there is no release version to
compare and no asset that corresponds to it. Install a release, or rebuild with
`make build VERSION=vX.Y.Z`.

If the binary's directory is not writable, `--update` saves the verified download
to a temp path and prints a `sudo install` line, or you can point at the release
asset directly.

## Troubleshooting

### `opencode-limits: command not found`

The binary is installed but its directory is not on `PATH`. Ask the shell where
it is looking:

```bash
echo "$PATH" | tr ':' '\n' | grep -E '\.local/bin|/usr/local/bin'
```

Add the directory for the current shell to test, then persist it in the startup
file for that shell:

```bash
export PATH="$HOME/.local/bin:$PATH"   # this shell only
```

Note that `~/.bashrc` runs for interactive non-login shells, `~/.bash_profile`
for login shells, and zsh reads `~/.zshrc`. Adding the line to the wrong one is
the usual reason a fix "does not work". `fish` needs `fish_add_path` instead.

### `install.sh: error: cannot write to ... (permission denied)`

The resolved directory is not writable by you. Either pick a writable one or use
a prefix you own:

```bash
curl -fsSL https://raw.githubusercontent.com/Cydnirn/opencode-limits/main/install.sh \
  | PREFIX="$HOME/.local" sh
```

Choosing `/usr/local/bin` explicitly means running the script with the privileges
to write there. The script only selects `/usr/local/bin` on its own when it is
already writable, so this error normally means `BINDIR` or `PREFIX` named a
directory you cannot write.

### `checksum mismatch`

The download does not match the published digest. The script installs nothing in
this case. Causes, in order of likelihood: a truncated transfer, a proxy or
captive portal serving an HTML error page, or a genuine tampering attempt.
Retry first; if it recurs, do not run the file. Download by hand and compare:

```bash
sha256sum opencode-limits_v0.1.1_linux_amd64
```

against the contents of the `.sha256` file. Report a persistent mismatch as a
security issue.

### The installer exits 1 and mentions Windows

`install.sh` does not support Windows. Use the `.exe` from the Releases page or
`go install`, as described in [Windows](#windows).

### `go install` fails with "module declares its path as ..."

The module path is lowercase. This exact error:

```
module declares its path as: github.com/cydnirn/opencode-limits
        but was required as: github.com/Cydnirn/opencode-limits
```

means the capitalised URL was used. Go module paths are case-sensitive:

```bash
go install github.com/cydnirn/opencode-limits/cmd/opencode-limits@latest
```

### `flag provided but not defined: -check-update`

The binary predates the self-update feature. v0.1.1 does not have these flags;
they arrive in the release that follows it. Until that release exists, build
from source (`make build`), which does include them. A binary reporting `dev`
has no version to compare and refuses to update either way.

### `not signed in to OpenCode Console`

The tool looked in the path for this OS (see [Credential and config
locations](#credential-and-config-locations)) and found nothing. The usual cause
is `opencode` never having been signed in on this machine, or a different
`XDG_DATA_HOME` / `LOCALAPPDATA` than the CLI used. The message names both ways
out: `opencode auth login`, or `opencode-limits --login` for the same device-code
flow without the CLI.
