#!/bin/sh
# Public fleet bootstrap. Trust comes from this script, never from the download.
set -eu
umask 077
REPO=blackcode-ch/tmx-go-public
KEY_ID=fleet-release-v1
# Public pin from docs/RECOVERY.md; SHA256 fingerprint: 3cf7e2aa36fa.
PUBLIC_KEY=6ec2frMWg6H02qJcMJWdxyJP6mSjQsojBsyW3/3/42c=
LABEL=ch.blackcode.tmxg
fail() { printf 'Tiamatrix: %s\n' "$*" >&2; exit 1; }
say() { printf 'Tiamatrix: %s\n' "$*"; }
mode=install
case ${1-} in
    '') ;;
    --uninstall) mode=uninstall ;;
    --help) echo 'Usage: sh install-macos.sh [--uninstall]'; exit 0 ;;
    *) fail 'Unknown option. Use --help.' ;;
esac
[ "$#" -le 1 ] || fail 'Too many arguments.'
[ "$(uname -s)" = Darwin ] || fail 'This installer requires macOS.'
[ "$(id -u)" != 0 ] || fail 'Run as your normal Mac user, without sudo.'
: "${HOME:?HOME must be set}"
case $HOME in /*) ;; *) fail 'HOME must be an absolute path.' ;; esac
bin_dir=$HOME/.local/bin
binary=$bin_dir/tmxg
agents=$HOME/Library/LaunchAgents
plist=$agents/$LABEL.plist
domain=gui/$(id -u)
# A custom state home must be configured explicitly by the owner, not silently lost.
[ -z "${TMXG_HOME-}" ] || fail 'TMXG_HOME is set. Use a separately configured LaunchAgent for that state directory.'
unload() {
    if launchctl print "$domain/$LABEL" >/dev/null 2>&1; then
        launchctl bootout "$domain/$LABEL" || fail 'Could not stop the LaunchAgent; files were not removed.'
    fi
}
if [ "$mode" = uninstall ]; then
    unload
    rm -f "$plist" "$binary"
    say 'Removed the user LaunchAgent and ~/.local/bin/tmxg.'
    say 'Your data, configuration, and rollback binaries were kept.'
    exit 0
fi
case $(uname -m) in
    arm64) arch=arm64 ;;
    x86_64)
        arch=amd64
        # A shell running under Rosetta still needs the native Apple Silicon build.
        if [ "$(sysctl -in hw.optional.arm64 2>/dev/null || true)" = 1 ]; then arch=arm64; fi ;;
    *) fail 'This Mac architecture is not supported.' ;;
esac
for cmd in curl plutil shasum xattr codesign launchctl; do
    command -v "$cmd" >/dev/null 2>&1 || fail "Required command is missing: $cmd"
done
ssl=
for candidate in /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl openssl; do
    if "$candidate" list -public-key-algorithms 2>/dev/null | grep -qi ED25519; then ssl=$candidate; break; fi
done
swift=/usr/bin/swift
[ -n "$ssl" ] || [ -x "$swift" ] || fail 'Ed25519 verification needs OpenSSL 3 or Swift with CryptoKit. Install it with brew install openssl@3, then retry.'
tmp=$(mktemp -d "${TMPDIR:-/tmp}/tmx-install.XXXXXXXX")
stage=
cleanup() { rm -rf "$tmp"; if [ -n "$stage" ]; then rm -f "$stage"; fi; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
download() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 20 --max-time 300 --retry 2 "$1" -o "$2"
}
# Releases are published with --latest=false, so /releases/latest is unsuitable.
download "https://api.github.com/repos/$REPO/releases?per_page=100" "$tmp/releases.json"
tag=
i=0
while [ "$i" -lt 100 ]; do
    candidate=$(plutil -extract "$i.tag_name" raw -o - "$tmp/releases.json" 2>/dev/null) || break
    draft=$(plutil -extract "$i.draft" raw -o - "$tmp/releases.json")
    pre=$(plutil -extract "$i.prerelease" raw -o - "$tmp/releases.json")
    if [ "$draft" = false ] && [ "$pre" = false ] && printf '%s\n' "$candidate" | grep -Eq '^fleet-v[0-9]+\.[0-9]+\.[0-9]+$'; then
        tag=$candidate; break
    fi
    i=$((i + 1))
done
[ -n "$tag" ] || fail 'No stable fleet release was found in the 100 newest releases.'
asset=tmxg_${tag}_darwin_${arch}
base=https://github.com/$REPO/releases/download/$tag
say "Downloading $tag for $arch."
download "$base/SHA256SUMS" "$tmp/SHA256SUMS"
download "$base/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig"
if [ -n "$ssl" ]; then
    # Ed25519 SubjectPublicKeyInfo DER header followed by the pinned raw public key.
    printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "$tmp/key.der"
    printf '%s' "$PUBLIC_KEY" | "$ssl" base64 -d -A >> "$tmp/key.der"
else
    cat > "$tmp/verify.swift" <<'SWIFT'
import Foundation
import CryptoKit

// Exit 3 means a well-formed but nonmatching signature; all errors fail closed.
guard CommandLine.arguments.count == 7 else { exit(2) }
let args = CommandLine.arguments
guard let signature = Data(base64Encoded: args[1]), !signature.isEmpty,
      signature.base64EncodedString() == args[1] else { exit(2) }
guard args[2] == "ed25519" else { exit(3) }
guard signature.count == 64 else { exit(2) }
guard args[3] == args[4] else { exit(3) }
do {
    guard let rawKey = Data(base64Encoded: args[5]) else { exit(2) }
    let key = try Curve25519.Signing.PublicKey(rawRepresentation: rawKey)
    let message = try Data(contentsOf: URL(fileURLWithPath: args[6]))
    exit(key.isValidSignature(signature, for: message) ? 0 : 3)
} catch {
    exit(2)
}
SWIFT
fi
{ printf 'tmx-fleet-release-v1\n%s\n%s\n' "$REPO" "$tag"; cat "$tmp/SHA256SUMS"; } > "$tmp/message"
# Accept the updater's typed and legacy Ed25519 lines; reject malformed files.
awk -F: '
    NF == 0 {next}
    NF != 2 && NF != 3 {exit 1}
    $1 == "" || $2 == "" {exit 1}
    {n++; p=$NF; if (p !~ /^[A-Za-z0-9+\/=]+$/) exit 1}
    END {if (n < 1 || n > 8) exit 1}
' "$tmp/SHA256SUMS.sig" || fail 'Malformed release signature file.'
valid=false
while IFS=: read -r key kind payload || [ -n "$key" ]; do
    [ -n "$key" ] || continue
    if [ -z "$payload" ]; then payload=$kind; kind=ed25519; fi
    if [ -z "$ssl" ]; then
        if "$swift" "$tmp/verify.swift" "$payload" "$kind" "$key" "$KEY_ID" "$PUBLIC_KEY" "$tmp/message"; then
            valid=true
        else
            result=$?
            [ "$result" = 3 ] || fail 'Swift signature verification failed. Nothing was installed.'
        fi
        continue
    fi
    printf '%s' "$payload" | "$ssl" base64 -d -A > "$tmp/signature"
    [ -s "$tmp/signature" ] || fail 'Empty signature payload.'
    [ "$("$ssl" base64 -A -in "$tmp/signature")" = "$payload" ] || fail 'Malformed signature encoding.'
    [ "$kind" = ed25519 ] || continue
    [ "$(wc -c < "$tmp/signature" | tr -d ' ')" = 64 ] || fail 'Malformed Ed25519 signature.'
    [ "$key" = "$KEY_ID" ] || continue
    if "$ssl" pkeyutl -verify -pubin -keyform DER -inkey "$tmp/key.der" -rawin \
        -in "$tmp/message" -sigfile "$tmp/signature" >/dev/null 2>&1; then valid=true; fi
done < "$tmp/SHA256SUMS.sig"
[ "$valid" = true ] || fail 'Release signature did not verify. Nothing was installed.'
expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name {n++; hash=$1} END {if(n != 1) exit 1; print hash}' "$tmp/SHA256SUMS") || fail 'The checksum must list this binary exactly once.'
printf '%s\n' "$expected" | grep -Eq '^[0-9a-fA-F]{64}$' || fail 'Invalid checksum.'
download "$base/$asset" "$tmp/binary"
actual=$(shasum -a 256 "$tmp/binary" | awk '{print $1}')
[ "$actual" = "$(printf '%s' "$expected" | tr 'A-F' 'a-f')" ] || fail 'Binary checksum did not match. Nothing was installed.'
say 'Verified the release signature and binary checksum.'
chmod 755 "$tmp/binary"
xattr -c "$tmp/binary"
codesign -s - --force "$tmp/binary"
codesign --verify "$tmp/binary"
# Escape paths for XML rather than interpolating user-controlled markup.
xml() { printf '%s' "$1" | sed 's/\&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g;'; }
cat > "$tmp/agent.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$LABEL</string>
<key>ProgramArguments</key><array><string>$(xml "$binary")</string><string>daemon</string></array>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>$(xml "$HOME")</string></dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>10</integer>
</dict></plist>
EOF
plutil -lint "$tmp/agent.plist" >/dev/null
mkdir -p "$bin_dir" "$agents"
if [ -L "$binary" ] || [ -L "$plist" ]; then
    fail 'Refusing to replace a symlink at the install destination.'
fi
# Stage on the destination filesystem, so the final binary rename is atomic.
stage=$(mktemp "$bin_dir/.tmxg-install.XXXXXXXX")
cp "$tmp/binary" "$stage"
chmod 755 "$stage"
if [ -f "$binary" ]; then
    old_hash=$(shasum -a 256 "$binary" | awk '{print $1}')
    backup=$binary.prev-$old_hash
    [ ! -L "$backup" ] || fail 'Rollback destination is a symlink.'
    if [ ! -e "$backup" ]; then cp -p "$binary" "$backup"; fi
    say "Kept the previous binary at $backup."
fi
unload
mv -f "$stage" "$binary"
stage=
cp "$tmp/agent.plist" "$plist"
chmod 644 "$plist"
launchctl enable "$domain/$LABEL"
launchctl bootstrap "$domain" "$plist" || fail 'Installed, but autostart could not load. Log in to a desktop session and retry. The previous binary is retained.'
say "Installed $tag at $binary and enabled autostart at login."
say 'Removed quarantine and applied an ad-hoc code signature.'
say 'Existing data and configuration were kept. Daemon startup uses your existing state.'
say 'Use ~/.local/bin/tmxg status to check the daemon.'
