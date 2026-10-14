#!/usr/bin/env sh
# Cross-compiles release binaries into dist/.
# Usage: scripts/build-release.sh v1.2.3
# Asset names end in <os>-<arch> so `gh extension install` picks the right one.
# On macOS it also makes a universal (Intel + Apple Silicon) binary with lipo.
set -eu

version=${1:?usage: build-release.sh <version>}
out=dist
rm -rf "$out"
mkdir -p "$out"

ldflags="-s -w -X github.com/MehdiMamas/gh-runners/internal/cli.Version=$version"
# The OAuth app behind "Sign in with GitHub" in the dashboard. Its client ID is
# public; without one the dashboard offers token sign-in only.
if [ -n "${GH_RUNNERS_OAUTH_CLIENT_ID:-}" ]; then
	ldflags="$ldflags -X github.com/MehdiMamas/gh-runners/internal/github.OAuthClientID=$GH_RUNNERS_OAUTH_CLIENT_ID"
fi
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64; do
	os=${target%/*}
	arch=${target#*/}
	ext=""
	[ "$os" = windows ] && ext=.exe
	echo "building $os/$arch"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 \
		go build -trimpath -ldflags "$ldflags" -o "$out/gh-runners-$os-$arch$ext" .
done

if command -v lipo >/dev/null 2>&1; then
	lipo -create -output "$out/gh-runners-darwin-universal" "$out/gh-runners-darwin-amd64" "$out/gh-runners-darwin-arm64"
	# Re-sign ad hoc so Apple Silicon accepts the merged binary.
	codesign --force --sign - "$out/gh-runners-darwin-universal"
	for f in "$out"/gh-runners-darwin-*; do codesign --force --sign - "$f"; done
fi

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum gh-runners-* >checksums.txt
else
	shasum -a 256 gh-runners-* >checksums.txt
fi
cat checksums.txt
