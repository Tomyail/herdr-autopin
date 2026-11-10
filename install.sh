#!/bin/sh
# herdr-autopin installer: puts the binary in place and runs it as a macOS
# LaunchAgent (starts at login, restarts when it exits).
#
#   curl -fsSL https://raw.githubusercontent.com/Tomyail/herdr-autopin/main/install.sh | sh
#   curl -fsSL ... | TYPESAFE_API_KEY=sk-... sh
#   ./install.sh --key sk-...            install with this API key
#   ./install.sh --uninstall             stop and remove the service
#   ./install.sh --uninstall --purge     also delete the binary
#   ./install.sh --dry                   install but pass -dry to the service
#   ./install.sh -- --only herdr.space=abc    extra flags for the binary
#
# Env: TYPESAFE_API_KEY, HERDR_AUTOPIN_DIR (default ~/.local/bin),
#      HERDR_AUTOPIN_VERSION (default: latest release), GITHUB_TOKEN if the repo is private.
set -eu

REPO=${HERDR_AUTOPIN_REPO:-Tomyail/herdr-autopin}
LABEL=com.tomyail.herdr-autopin
BIN_DIR=${HERDR_AUTOPIN_DIR:-$HOME/.local/bin}
BIN=$BIN_DIR/herdr-autopin
PLIST=$HOME/Library/LaunchAgents/$LABEL.plist
LOG_DIR=$HOME/Library/Logs
SERVICE_ARGS=""
KEY=${TYPESAFE_API_KEY:-}
DO_DRY=0

die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }

while [ $# -gt 0 ]; do
	case $1 in
		--key) [ $# -ge 2 ] || die "--key needs a value"; KEY=$2; shift 2 ;;
		--dir) [ $# -ge 2 ] || die "--dir needs a value"; BIN_DIR=$2; BIN=$BIN_DIR/herdr-autopin; shift 2 ;;
		--version) [ $# -ge 2 ] || die "--version needs a value"; HERDR_AUTOPIN_VERSION=$2; shift 2 ;;
		--dry) DO_DRY=1; shift ;;
		--uninstall) UNINSTALL=1; shift ;;
		--purge) PURGE=1; shift ;;
		--no-service) NO_SERVICE=1; shift ;;
		--) shift; SERVICE_ARGS=$*; break ;;
		-h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "unknown option: $1 (try --help)" ;;
	esac
done
[ "$DO_DRY" = 1 ] && SERVICE_ARGS="$SERVICE_ARGS -dry"
UNINSTALL=${UNINSTALL:-0}
PURGE=${PURGE:-0}
NO_SERVICE=${NO_SERVICE:-0}

# --- uninstall --------------------------------------------------------------
if [ "$UNINSTALL" = 1 ]; then
	launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
	rm -f "$PLIST"
	say "service $LABEL removed"
	if [ "$PURGE" = 1 ]; then
		rm -f "$BIN"
		say "binary removed: $BIN"
	else
		say "binary kept: $BIN (add --purge to delete it)"
	fi
	exit 0
fi

[ "$(uname -s)" = Darwin ] || die "the service part is macOS-only; build with 'go build ./cmd/herdr-autopin' elsewhere"
[ "$NO_SERVICE" = 0 ] && [ -z "$KEY" ] && die "no API key: pass --key sk-... or export TYPESAFE_API_KEY"

# --- fetch the binary -------------------------------------------------------
os=darwin
case $(uname -m) in
	arm64) arch=arm64 ;;
	x86_64) arch=amd64 ;;
	*) die "unsupported CPU: $(uname -m)" ;;
esac

version=${HERDR_AUTOPIN_VERSION:-}
if [ -z "$version" ]; then
	version=$(curl -fsSL ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} \
		"https://api.github.com/repos/$REPO/releases/latest" \
		| sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1) || true
fi
[ -n "$version" ] || die "no release found for $REPO (set HERDR_AUTOPIN_VERSION, or build with 'make dev')"

tarball="herdr-autopin-${version}-${os}-${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/${version}/${tarball}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "downloading $tarball"
if ! curl -fsSL -o "$tmp/$tarball" "$url"; then
	# No release yet, but this may be a git checkout with Go at hand.
	if command -v go >/dev/null 2>&1 && [ -f "$(dirname "$0")/go.mod" ]; then
		say "release download failed; building from source"
		( cd "$(dirname "$0")" && go build -trimpath -ldflags "-X main.version=local" -o "$tmp/herdr-autopin" ./cmd/herdr-autopin )
	else
		die "download failed: $url"
	fi
fi

if [ -f "$tmp/herdr-autopin" ]; then
	bin=$tmp/herdr-autopin
else
	if curl -fsSL -o "$tmp/$tarball.sha256" "$url.sha256" 2>/dev/null; then
		( cd "$tmp" && { shasum -a 256 -c "$tarball.sha256" >/dev/null 2>&1 || sha256sum -c "$tarball.sha256" >/dev/null; } ) \
			|| die "checksum mismatch for $tarball"
	fi
	tar -C "$tmp" -xzf "$tmp/$tarball"
	bin=$(find "$tmp" -type f -name herdr-autopin -perm -u+x | head -1)
	[ -n "$bin" ] || die "no herdr-autopin inside $tarball"
fi

mkdir -p "$BIN_DIR" "$LOG_DIR"
install -m 0755 "$bin" "$BIN"
say "installed $("$BIN" -version 2>/dev/null || echo '?') at $BIN"

[ "$NO_SERVICE" = 1 ] && { say "skipped the service (--no-service)"; exit 0; }

# --- run it as a LaunchAgent ------------------------------------------------
mkdir -p "$(dirname "$PLIST")"
tmp_plist=$tmp/plist
{
	cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN</string>
EOF
	for a in $SERVICE_ARGS; do printf '\t\t<string>%s</string>\n' "$a"; done
	cat <<EOF
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>TYPESAFE_API_KEY</key>
		<string>$KEY</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/herdr-autopin.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/herdr-autopin.log</string>
</dict>
</plist>
EOF
} > "$tmp_plist"

plutil -lint "$tmp_plist" >/dev/null || die "generated plist is invalid"
install -m 0600 "$tmp_plist" "$PLIST"   # 0600: it holds the API key
say "wrote $PLIST"

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl kickstart -k "gui/$(id -u)/$LABEL"
sleep 1

if launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
	say "service $LABEL is running; log: $LOG_DIR/herdr-autopin.log"
	tail -n 3 "$LOG_DIR/herdr-autopin.log" 2>/dev/null | sed 's/^/  /' || true
else
	die "service did not start; check $LOG_DIR/herdr-autopin.log"
fi
