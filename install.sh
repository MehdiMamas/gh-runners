#!/usr/bin/env sh
# Installs gh-runners on macOS or Linux:
#   curl -fsSL https://raw.githubusercontent.com/MehdiMamas/gh-runners/main/install.sh | sh
# Options (environment): GH_RUNNERS_VERSION=v0.1.0, GH_RUNNERS_BIN=~/.local/bin
# To join a main host, add the code from Add a machine on its dashboard:
#   curl -fsSL .../install.sh | sh -s -- --join <code>
set -eu

join=${GH_RUNNERS_JOIN:-}
while [ $# -gt 0 ]; do
	case "$1" in
	--join) [ $# -ge 2 ] || { echo "gh-runners: --join needs the code" >&2; exit 1; }; join=$2; shift 2 ;;
	--join=*) join=${1#--join=}; shift ;;
	*) echo "gh-runners: unknown option $1" >&2; exit 1 ;;
	esac
done

repo=MehdiMamas/gh-runners
bin=${GH_RUNNERS_BIN:-$HOME/.local/bin}
version=${GH_RUNNERS_VERSION:-latest}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) echo "gh-runners: unsupported OS $(uname -s); on Windows use install.ps1" >&2; exit 1 ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
armv7* | armv6*) arch=arm ;;
*) echo "gh-runners: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi
asset="gh-runners-$os-$arch"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset ($version) ..."
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
	echo "gh-runners: checksum mismatch for $asset (want ${want:-none}, got $got)" >&2
	exit 1
fi

mkdir -p "$bin"
chmod +x "$tmp/$asset"
# Replace via rename so a running daemon keeps its old copy until restarted.
mv -f "$tmp/$asset" "$bin/gh-runners"
echo "Installed $bin/gh-runners"
"$bin/gh-runners" --version

case ":$PATH:" in
*":$bin:"*) ;;
*) echo "Note: $bin is not on your PATH. Add this to your shell profile:"
	echo "  export PATH=\"$bin:\$PATH\"" ;;
esac
if "$bin/gh-runners" service status 2>/dev/null | grep -q "running: *true"; then
	echo "Restarting the background service to load the new version ..."
	"$bin/gh-runners" service stop && "$bin/gh-runners" service start
fi
# Best effort: an app-menu entry that opens the dashboard.
"$bin/gh-runners" shortcut install >/dev/null 2>&1 || true
if [ -n "$join" ]; then
	# Pairs with the main host and starts the daemon at login.
	"$bin/gh-runners" join "$join"
	echo "Done: the main host manages this machine now."
else
	echo "Next: run gh-runners to open the dashboard in your browser"
fi
