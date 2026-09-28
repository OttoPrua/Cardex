#!/bin/sh
# Install Cardex and its dispatch skill without changing provider or shell config.
set -eu
manager=codex
version=
archive_dir=
skill_dir=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --manager|--version|--archive-dir|--skill-dir)
            [ "$#" -ge 2 ] || { echo "Missing value for $1" >&2; exit 1; }
            case "$1" in
                --manager) manager=$2 ;;
                --version) version=${2#v} ;;
                --archive-dir) archive_dir=$2 ;;
                --skill-dir) skill_dir=$2 ;;
            esac
            shift 2 ;;
        --help|-h)
            echo 'Usage: install.sh [--manager codex|hermes] [--version VERSION] [--archive-dir DIR] [--skill-dir SKILLS_ROOT]'
            exit 0 ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done
case "$manager" in codex|hermes) ;; *) echo 'Manager must be codex or hermes' >&2; exit 1 ;; esac
case "$(uname -s)" in Darwin) system=darwin ;; Linux) system=linux ;; *) echo 'Use install.ps1 on Windows' >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo 'Unsupported architecture' >&2; exit 1 ;; esac
case "$version" in *[!0-9A-Za-z.+_-]*) echo 'Invalid version' >&2; exit 1 ;; esac
base=https://github.com/OttoPrua/Cardex/releases/latest/download
[ -z "$version" ] || base="https://github.com/OttoPrua/Cardex/releases/download/v$version"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/cardex-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
fetch() {
    if [ -n "$archive_dir" ]; then cp "$archive_dir/$1" "$tmp/$1"
    else curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' "$base/$1" -o "$tmp/$1"
    fi
}
fetch SHA256SUMS
if [ -n "$version" ]; then
    asset="cardex_${version}_${system}_${arch}.tar.gz"
else
    asset=$(awk -v suffix="_${system}_${arch}.tar.gz" '$2 ~ /^cardex_[0-9A-Za-z.+_-]+$/ && substr($2,length($2)-length(suffix)+1)==suffix { print $2 }' "$tmp/SHA256SUMS")
    [ -n "$asset" ] && [ "$(printf '%s\n' "$asset" | wc -l | tr -d ' ')" = 1 ] || { echo 'Expected exactly one matching release archive in SHA256SUMS' >&2; exit 1; }
fi
expected=$(awk -v asset="$asset" '$2==asset {print $1}' "$tmp/SHA256SUMS")
[ "${#expected}" = 64 ] || { echo 'Missing or invalid SHA256SUMS entry' >&2; exit 1; }
case "$expected" in *[!0-9a-fA-F]*) echo 'Invalid SHA256SUMS digest' >&2; exit 1 ;; esac
fetch "$asset"
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo 'SHA256 mismatch; installation was not changed' >&2; exit 1; }
# Extract only the two required regular files, never archive-supplied paths/links.
tar -xOf "$tmp/$asset" cardex > "$tmp/cardex"
mkdir "$tmp/skill"
tar -xOf "$tmp/$asset" skills/cardex-dispatch/SKILL.md > "$tmp/skill/SKILL.md"
[ -s "$tmp/cardex" ] && [ -s "$tmp/skill/SKILL.md" ] || { echo 'Release is missing Cardex or the dispatch skill' >&2; exit 1; }
bin_dir="$HOME/.local/bin"
if [ -n "$skill_dir" ]; then skill_root=$skill_dir
elif [ "$manager" = codex ]; then skill_root="$HOME/.agents/skills"
else skill_root="${HERMES_HOME:-$HOME/.hermes}/skills"
fi
backup_dir="$HOME/.local/state/cardex/backups"
backup_suffix="backup-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$bin_dir" "$skill_root"
if [ -e "$bin_dir/cardex" ]; then
    cp -p "$bin_dir/cardex" "$bin_dir/cardex.$backup_suffix"
    echo "Previous binary: $bin_dir/cardex.$backup_suffix"
fi
cp "$tmp/cardex" "$bin_dir/.cardex-install-$$"
chmod 755 "$bin_dir/.cardex-install-$$"
mv -f "$bin_dir/.cardex-install-$$" "$bin_dir/cardex"
if [ -e "$skill_root/cardex-dispatch" ]; then
    if ! diff -r "$tmp/skill" "$skill_root/cardex-dispatch" >/dev/null 2>&1; then
        mkdir -p "$backup_dir"
        mv "$skill_root/cardex-dispatch" "$backup_dir/cardex-dispatch-$manager.$backup_suffix"
        cp -R "$tmp/skill" "$skill_root/cardex-dispatch"
        echo "Previous skill: $backup_dir/cardex-dispatch-$manager.$backup_suffix"
    fi
else cp -R "$tmp/skill" "$skill_root/cardex-dispatch"
fi
printf '\nInstalled: %s\nDispatch skill: %s\n' "$bin_dir/cardex" "$skill_root/cardex-dispatch"
printf 'For this terminal, run: export PATH="$HOME/.local/bin:$PATH"\n'
printf 'In your %s management session, ask: Use cardex-dispatch to guide my subscriptions and recommend a dispatch preset.\n\n' "$manager"
"$bin_dir/cardex" setup -inventory
