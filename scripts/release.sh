#!/bin/bash
# Build release archives for every platform: scripts/release.sh v2.2.2 <outdir>
# Each archive holds the binary, README, LICENSE, docs/ and skills/.
set -euo pipefail
ver=${1:?version, e.g. v2.2.2}
out=${2:?output dir}
root=$(cd "$(dirname "$0")/.." && pwd)
rev=$(git -C "$root" rev-parse --short=12 HEAD)
export COPYFILE_DISABLE=1 # no AppleDouble ._ files from macOS tar
mkdir -p "$out"
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*} arch=${target#*/}
  name=cardex_${ver}_${os}_${arch}
  stage="$out/$name"
  rm -rf "$stage" && mkdir -p "$stage"
  exe=cardex; [ "$os" = windows ] && exe=cardex.exe
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$ver -X main.revision=$rev" -o "$stage/$exe" ./cmd/cardex)
  cp "$root"/README.md "$root"/README.en.md "$root"/LICENSE "$stage/"
  rsync -a --exclude '._*' --exclude '.DS_Store' "$root/docs" "$root/skills" "$stage/"
  if [ "$os" = windows ]; then
    (cd "$out" && rm -f "$name.zip" && zip -qr -X "$name.zip" "$name")
  else
    # COPYFILE_DISABLE omits AppleDouble files, but macOS tar can still
    # record xattrs (notably com.apple.provenance) as PAX headers.
    (cd "$out" && tar --no-xattrs -czf "$name.tar.gz" "$name")
  fi
done
(cd "$out" && shasum -a 256 cardex_${ver}_*.tar.gz cardex_${ver}_*.zip > SHA256SUMS)
echo "built $ver ($rev) in $out"
