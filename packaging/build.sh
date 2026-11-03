#!/bin/bash
# Builds Agent Mask for every platform (Go 1.24+):
#   packaging/out/AgentMask-Setup-<ver>.exe              Windows x64 installer (needs makensis)
#   packaging/out/agent-mask-<ver>-windows-amd64.zip     the same, without installer
#   packaging/out/agent-mask-<ver>-<os>-<arch>.tar.gz    macOS (arm64, amd64), Linux (amd64, arm64)
#   packaging/out/SHA256SUMS.txt
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; APP="$(cd "$HERE/.." && pwd)"; OUT="$HERE/out"
VER="${VER:-$(grep -oE 'var version = "[^"]+"' "$APP/cmd/agent-mask/main.go" | cut -d'"' -f2)}"
rm -rf "$OUT"; mkdir -p "$OUT"
build() { # os arch dir
	( cd "$APP" && CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "-s -w -X main.version=$VER" -o "$3/agent-mask$( [ "$1" = windows ] && echo .exe )" ./cmd/agent-mask )
	cp "$APP/README.md" "$APP/LICENSE" "$3/"
}
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os=${t%/*}; arch=${t#*/}; d="$OUT/agent-mask-$VER-$os-$arch"
	build "$os" "$arch" "$d"
	tar -C "$OUT" -czf "$d.tar.gz" "$(basename "$d")" && rm -rf "$d"
done
W="$OUT/agent-mask-$VER-windows-amd64"; build windows amd64 "$W"
cp "$HERE"/windows/{path.ps1,setup-deps.ps1} "$W/"
( cd "$OUT" && zip -qr "$(basename "$W").zip" "$(basename "$W")" )
if command -v makensis >/dev/null; then
	cp "$HERE/windows/installer.nsi" "$W/"
	( cd "$W" && makensis -V2 -DVER="$VER" installer.nsi ) && mv "$W"/AgentMask-Setup-*.exe "$OUT/"
fi
rm -rf "$W"
( cd "$OUT" && sha256sum *.zip *.tar.gz *.exe 2>/dev/null > SHA256SUMS.txt )
ls -la "$OUT"
