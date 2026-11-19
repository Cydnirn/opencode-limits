#!/bin/sh
# install.sh - download and install the opencode-limits binary.
#
# Supports Linux and macOS on amd64/arm64. Requires only curl, a SHA-256 tool
# (sha256sum or shasum) and a POSIX shell. No Go toolchain, git or python.
#
# Progress goes to stderr; the installed path alone goes to stdout, so
#   dir=$(./install.sh)   # captures only the path
# works as expected.
#
# Environment:
#   VERSION   pin a release, e.g. VERSION=v0.1.1 (default: latest)
#   BINDIR    install here (wins over PREFIX)
#   PREFIX    install into $PREFIX/bin
#
# Written for POSIX sh and tested under dash: no arrays, no `local`,
# no bash-only expansions.

set -eu

REPO="Cydnirn/opencode-limits"
BIN_NAME="opencode-limits"
RELEASES_URL="https://github.com/${REPO}/releases"

# Scratch state, initialised so the cleanup trap is safe under `set -u`.
tmpdir=""
tmpdest=""

usage() {
	cat <<EOF
Usage: install.sh [--dry-run] [--uninstall] [--help]

Download the latest ${BIN_NAME} release from GitHub, verify its SHA-256
checksum, and install it onto your PATH.

Options:
  --dry-run     Resolve the version and the asset, download and verify the
                checksum, then report what would happen. Writes nothing
                outside a temporary directory.
  --uninstall   Remove the installed binary, if present.
  --help        Show this message and exit.

Environment:
  VERSION       Pin a specific release instead of the latest (e.g. VERSION=v0.1.1).
  BINDIR        Install directory. Takes precedence over PREFIX.
  PREFIX        Install into \$PREFIX/bin. Takes precedence over the defaults.

Install directory, first match wins:
  1. \$BINDIR
  2. \$PREFIX/bin
  3. /usr/local/bin, if it exists and the current user can write to it
  4. \$HOME/.local/bin

On Windows, download the .exe from ${RELEASES_URL}/latest manually.
EOF
}

log() {
	printf '%s\n' "$*" >&2
}

die() {
	printf 'install.sh: error: %s\n' "$*" >&2
	exit 1
}

cleanup() {
	if [ -n "$tmpdir" ]; then
		rm -rf "$tmpdir" || true
	fi
	if [ -n "$tmpdest" ]; then
		rm -f "$tmpdest" || true
	fi
}

trap cleanup 0
trap 'exit 130' 1 2 3 15

require_curl() {
	command -v curl >/dev/null 2>&1 ||
		die "curl is required but was not found on PATH. Install curl and retry."
}

# sha256_of <file> -> 64-char lowercase hex digest on stdout.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		hex=$(sha256sum "$1")
	elif command -v shasum >/dev/null 2>&1; then
		hex=$(shasum -a 256 "$1")
	else
		die "neither sha256sum nor shasum found; cannot verify the download."
	fi
	# Both tools print "<hex>  <name>"; keep the digest only.
	printf '%s\n' "${hex%% *}"
}

detect_os() {
	uname_s=$(uname -s)
	case "$uname_s" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		printf 'install.sh: Windows is not supported by this installer.\n' >&2
		printf 'Download the .exe for your architecture from:\n  %s/latest\n' \
			"$RELEASES_URL" >&2
		exit 1
		;;
	*)
		die "unsupported operating system '$uname_s' (expected Linux or Darwin)."
		;;
	esac
}

detect_arch() {
	uname_m=$(uname -m)
	case "$uname_m" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported architecture '$uname_m' (expected x86_64/amd64 or aarch64/arm64)." ;;
	esac
}

# resolve_tag -> sets $tag to something like v0.1.1.
# Follows the /releases/latest redirect and reads the final effective URL,
# which avoids the api.github.com JSON endpoint (no token, no rate limit).
resolve_tag() {
	latest_url="${RELEASES_URL}/latest"
	effective=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$latest_url") ||
		die "could not reach $latest_url (network error or no releases)."

	case "$effective" in
	*/releases/tag/*) ;;
	*)
		die "unexpected redirect target '$effective' (no release tag in the URL)."
		;;
	esac

	tag=${effective##*/}
	case "$tag" in
	v[0-9]*) ;;
	*) die "could not parse a version tag from '$effective'." ;;
	esac
}

validate_version() {
	case "$1" in
	v*) ;;
	*) die "VERSION must look like v0.1.1 (got '$1')." ;;
	esac
	# Keep the tag safe for use in a URL path.
	case "$1" in
	*[!A-Za-z0-9._-]*) die "VERSION contains unsupported characters: '$1'." ;;
	esac
}

# install_dir -> sets $dir. Does not create it.
install_dir() {
	if [ -n "${BINDIR:-}" ]; then
		dir=$BINDIR
		return
	fi
	if [ -n "${PREFIX:-}" ]; then
		dir="${PREFIX%/}/bin"
		return
	fi
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
		return
	fi
	if [ -z "${HOME:-}" ]; then
		die "HOME is unset; set BINDIR or PREFIX to choose an install directory."
	fi
	dir="${HOME}/.local/bin"
}

is_on_path() {
	case ":${PATH:-}:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

# tell_user_about_path <dir> - print a copy-pasteable line for their shell.
tell_user_about_path() {
	target_dir=$1
	user_shell=""
	if [ -n "${SHELL:-}" ]; then
		user_shell=${SHELL##*/}
	fi

	case "$user_shell" in
	fish)
		log ""
		log "$target_dir is not on your PATH. Add it with:"
		log "  fish_add_path $target_dir"
		;;
	zsh)
		log ""
		log "$target_dir is not on your PATH. Add it by appending this line to ${HOME}/.zshrc:"
		log "  export PATH=\"$target_dir:\$PATH\""
		log "Then restart your shell or run: . \"${HOME}/.zshrc\""
		;;
	bash)
		log ""
		log "$target_dir is not on your PATH. Add it by appending this line to ${HOME}/.bashrc:"
		log "  export PATH=\"$target_dir:\$PATH\""
		log "Then restart your shell or run: . \"${HOME}/.bashrc\""
		;;
	*)
		log ""
		log "$target_dir is not on your PATH. Add it to PATH, for example:"
		log "  export PATH=\"$target_dir:\$PATH\""
		log "Put that line in your shell's startup file (e.g. ~/.bashrc, ~/.zshrc,"
		log "or config.fish) to make it permanent."
		;;
	esac

	log ""
	log "Until then, run it as: $target_dir/$BIN_NAME"
}

do_uninstall() {
	install_dir
	binary="$dir/$BIN_NAME"

	if [ ! -e "$binary" ]; then
		log "$BIN_NAME is not installed at $binary; nothing to do."
		exit 0
	fi
	if [ -d "$binary" ]; then
		die "$binary is a directory, refusing to remove it."
	fi

	rm -f "$binary" || die "could not remove $binary (permission denied?)."
	log "Removed $binary"
	printf '%s\n' "$binary"
}

main() {
	dry_run=0
	uninstall=0

	while [ $# -gt 0 ]; do
		case "$1" in
		--dry-run) dry_run=1 ;;
		--uninstall) uninstall=1 ;;
		--help | -h)
			usage
			exit 0
			;;
		*)
			printf 'install.sh: error: unknown option %s\n\n' "$1" >&2
			usage >&2
			exit 2
			;;
		esac
		shift
	done

	if [ "$uninstall" -eq 1 ]; then
		do_uninstall
		return 0
	fi

	detect_os
	detect_arch
	require_curl

	if [ -n "${VERSION:-}" ]; then
		validate_version "$VERSION"
		tag=$VERSION
	else
		resolve_tag
	fi

	asset="${BIN_NAME}_${tag}_${os}_${arch}"
	sha_asset="${asset}.sha256"
	download_base="${RELEASES_URL}/download/${tag}"

	log "opencode-limits installer"
	log "  platform: ${os}/${arch}"
	log "  version:  ${tag}"
	log "  asset:    ${asset}"

	tmpdir=$(mktemp -d) || die "could not create a temporary directory."
	bin_path="$tmpdir/$asset"
	sha_path="$tmpdir/$sha_asset"

	log "Downloading ${download_base}/${asset}"
	curl -fsSL --retry 3 --retry-delay 1 -o "$bin_path" "${download_base}/${asset}" ||
		die "download failed: ${download_base}/${asset}"

	log "Downloading ${download_base}/${sha_asset}"
	curl -fsSL --retry 3 --retry-delay 1 -o "$sha_path" "${download_base}/${sha_asset}" ||
		die "download failed: ${download_base}/${sha_asset}"

	expected=$(head -n 1 "$sha_path" | cut -d' ' -f1)
	case "$expected" in
	????????????????????????????????????????????????????????????????) ;;
	*) die "malformed checksum file for ${asset} (expected a 64-character digest)." ;;
	esac

	log "Verifying SHA-256 checksum"
	actual=$(sha256_of "$bin_path")
	if [ "$expected" != "$actual" ]; then
		printf 'install.sh: error: checksum mismatch for %s\n' "$asset" >&2
		printf '  expected: %s\n  actual:   %s\n' "$expected" "$actual" >&2
		printf 'The download is corrupt or was tampered with. Nothing installed.\n' >&2
		exit 1
	fi
	log "  ok: $actual"

	# Catch a wrong-arch or truncated binary before it lands on PATH.
	chmod 0755 "$bin_path"
	if ! reported=$("$bin_path" --version 2>&1); then
		printf 'install.sh: error: the downloaded binary failed to run:\n%s\n' \
			"$reported" >&2
		printf 'Nothing installed.\n' >&2
		exit 1
	fi
	case "$reported" in
	*"$BIN_NAME"*) ;;
	*)
		printf 'install.sh: error: unexpected output from the downloaded binary:\n%s\n' \
			"$reported" >&2
		printf 'Expected it to identify itself as %s. Nothing installed.\n' \
			"$BIN_NAME" >&2
		exit 1
		;;
	esac
	log "  binary reports: $(printf '%s' "$reported" | head -n 1)"

	install_dir
	destination="$dir/$BIN_NAME"

	if [ "$dry_run" -eq 1 ]; then
		log ""
		log "Dry run: no files were written outside the temporary directory."
		log "  would install to: $destination"
		if is_on_path "$dir"; then
			log "  $dir is already on PATH"
		else
			log "  $dir is NOT on PATH (the real run would print instructions)"
		fi
		printf '%s\n' "$destination"
		return 0
	fi

	if [ ! -d "$dir" ]; then
		log "Creating $dir"
		mkdir -p "$dir" || die "could not create $dir (permission denied?)."
	fi
	[ -w "$dir" ] || die "cannot write to $dir (permission denied). Set BINDIR to a writable directory."

	# Stage next to the destination so the final mv is an atomic rename on
	# the same filesystem, never a half-written binary on PATH.
	tmpdest="$dir/.${BIN_NAME}.new.$$"
	log "Installing to $destination"
	cp "$bin_path" "$tmpdest" || die "could not copy the binary into $dir."
	chmod 0755 "$tmpdest"
	mv -f "$tmpdest" "$destination" || die "could not move the binary into place."
	tmpdest=""
	chmod 0755 "$destination"

	log "Installed $destination"

	if ! is_on_path "$dir"; then
		tell_user_about_path "$dir"
	fi

	printf '%s\n' "$destination"
}

main "$@"
