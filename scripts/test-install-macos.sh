#!/usr/bin/env bash
# Linux/Git Bash integration test: real Ed25519, fake transport and macOS services.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
mkdir -p "$test_dir/tools" "$test_dir/release" "$test_dir/home with & spaces"
export HOME="$test_dir/home with & spaces"
export FIXTURE="$test_dir/release" EVENTS="$test_dir/events" TEST_ARCH=arm64
export REAL_OPENSSL
REAL_OPENSSL=$(command -v openssl)
unset TMXG_HOME
if ! command -v shasum >/dev/null 2>&1; then
    cat > "$test_dir/tools/shasum" <<'EOF'
#!/bin/sh
shift 2
sha256sum "$@"
EOF
    chmod +x "$test_dir/tools/shasum"
fi
openssl genpkey -algorithm ED25519 -out "$test_dir/key" 2>/dev/null
pub=$(openssl pkey -in "$test_dir/key" -pubout -outform DER | tail -c 32 | openssl base64 -A)
# Test-only copy: production has no key, platform, or transport bypass switches.
sed "s|^PUBLIC_KEY=.*|PUBLIC_KEY=$pub|" "$root/scripts/install-macos.sh" > "$test_dir/install.sh"
cat > "$test_dir/tools/uname" <<'EOF'
#!/bin/sh
case "$1" in -s) echo Darwin;; -m) echo "$TEST_ARCH";; esac
EOF
cat > "$test_dir/tools/id" <<'EOF'
#!/bin/sh
echo 501
EOF
cat > "$test_dir/tools/sysctl" <<'EOF'
#!/bin/sh
echo "${ROSETTA:-0}"
EOF
cat > "$test_dir/tools/curl" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
    case "$1" in https://*) url=$1;; -o) shift; out=$1;; esac
    shift
done
case $url in
    *api.github.com*) printf 'fixture metadata' > "$out" ;;
    *) cp "$FIXTURE/${url##*/}" "$out" ;;
esac
EOF
cat > "$test_dir/tools/plutil" <<'EOF'
#!/bin/sh
case "$1:$2" in
    -lint:*) exit 0 ;;
    -extract:0.tag_name) echo "${TEST_TAG:-fleet-v1.2.3}" ;;
    -extract:0.draft|-extract:0.prerelease) echo false ;;
    *) exit 1 ;;
esac
EOF
cat > "$test_dir/tools/launchctl" <<'EOF'
#!/bin/sh
printf 'launchctl %s\n' "$*" >> "$EVENTS"
case "$1" in
    print) test -f "$EVENTS.loaded" ;;
    bootstrap) touch "$EVENTS.loaded" ;;
    bootout) rm -f "$EVENTS.loaded" ;;
esac
EOF
for tool in codesign xattr; do
    cat > "$test_dir/tools/$tool" <<'EOF'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$EVENTS"
EOF
done
chmod +x "$test_dir/tools/"*
export PATH="$test_dir/tools:$PATH"
sign() {
    { printf 'tmx-fleet-release-v1\nblackcode-ch/tmx-go-public\nfleet-v1.2.3\n'; cat "$FIXTURE/SHA256SUMS"; } > "$test_dir/message"
    printf 'fleet-release-v1:ed25519:' > "$FIXTURE/SHA256SUMS.sig"
    openssl pkeyutl -sign -inkey "$test_dir/key" -rawin -in "$test_dir/message" | openssl base64 -A >> "$FIXTURE/SHA256SUMS.sig"
    printf '\n' >> "$FIXTURE/SHA256SUMS.sig"
}
fixture() {
    printf 'not executable: arm64 signed fixture\n' > "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64"
    printf 'not executable: amd64 signed fixture\n' > "$FIXTURE/tmxg_fleet-v1.2.3_darwin_amd64"
    (cd "$FIXTURE" && shasum -a 256 tmxg_*) > "$FIXTURE/SHA256SUMS"
    sign
}
installer=$test_dir/install.sh
run() { sh "$installer" "$@" > "$test_dir/output" 2>&1; }
refuses() {
    cp "$HOME/.local/bin/tmxg" "$test_dir/before"
    cp "$HOME/Library/LaunchAgents/ch.blackcode.tmxg.plist" "$test_dir/plist-before"
    cp "$EVENTS" "$test_dir/events-before"
    if run; then echo 'ERROR: accepted bad release'; exit 1; fi
    cmp "$test_dir/before" "$HOME/.local/bin/tmxg"
    cmp "$test_dir/plist-before" "$HOME/Library/LaunchAgents/ch.blackcode.tmxg.plist"
    cmp "$test_dir/events-before" "$EVENTS"
}
fixture
mkdir -p "$HOME/Library/Application Support/tmxg-trial"
printf 'existing config and state sentinel' > "$HOME/Library/Application Support/tmxg-trial/config.json"
run || { cat "$test_dir/output"; exit 1; }
cmp "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64" "$HOME/.local/bin/tmxg"
grep -q 'home with &amp; spaces' "$HOME/Library/LaunchAgents/ch.blackcode.tmxg.plist"
grep -q 'codesign -s - --force' "$EVENTS"
grep -q 'launchctl bootstrap' "$EVENTS"
echo 'PASS: valid signature installs arm64, escapes paths, signs and starts agent'
export TEST_ARCH=x86_64
run
cmp "$FIXTURE/tmxg_fleet-v1.2.3_darwin_amd64" "$HOME/.local/bin/tmxg"
old_hash=$(shasum -a 256 "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64" | awk '{print $1}')
cmp "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64" "$HOME/.local/bin/tmxg.prev-$old_hash"
echo 'PASS: Intel upgrade preserves previous binary'
export ROSETTA=1
run
cmp "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64" "$HOME/.local/bin/tmxg"
echo 'PASS: Rosetta selects native arm64'
unset ROSETTA
export TEST_ARCH=arm64
cp "$FIXTURE/SHA256SUMS.sig" "$test_dir/good.sig"
sed 's/fleet-release-v1/unknown-key/' "$test_dir/good.sig" > "$FIXTURE/SHA256SUMS.sig"
refuses
echo 'PASS: wrong signing identity refuses without installation side effects'
cp "$test_dir/good.sig" "$FIXTURE/SHA256SUMS.sig"
printf 'tampered manifest\n' >> "$FIXTURE/SHA256SUMS"
refuses
echo 'PASS: invalid signature refuses'
fixture
printf 'tampered binary\n' >> "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64"
refuses
echo 'PASS: checksum mismatch refuses'
fixture
export TEST_TAG=fleet-v9.9.9
# Copy assets under the new tag: the unchanged signature must still be rejected.
refuses
unset TEST_TAG
echo 'PASS: signature is bound to release tag'
fixture
cat "$FIXTURE/SHA256SUMS" >> "$test_dir/duplicate"
cat "$test_dir/duplicate" >> "$FIXTURE/SHA256SUMS"
sign
refuses
echo 'PASS: signed duplicate checksum refuses'
fixture
sed 's/:ed25519:/:/' "$test_dir/good.sig" > "$FIXTURE/SHA256SUMS.sig"
run
echo 'PASS: updater legacy signature format accepted'
# Force verifier discovery in temporary copies, independently of host tools.
sed -e 's|^for candidate in .*; do|for candidate in /nonexistent/tmx-test-openssl; do|' \
    -e "s|^swift=.*|swift='$test_dir/tools/swift'|" "$test_dir/install.sh" > "$test_dir/swift-install.sh"
cat > "$test_dir/tools/swift" <<'EOF'
#!/bin/sh
# Linux stand-in for CryptoKit: verify the actual supplied key/message/signature.
set -eu
script=$1; payload=$2; kind=$3; key=$4; key_id=$5; pin=$6; message=$7
grep -q 'Curve25519.Signing.PublicKey(rawRepresentation: rawKey)' "$script"
grep -q 'key.isValidSignature(signature, for: message)' "$script"
printf '%s' "$payload" | "$REAL_OPENSSL" base64 -d -A > "$script.sig"
[ -s "$script.sig" ] || exit 2
[ "$("$REAL_OPENSSL" base64 -A -in "$script.sig")" = "$payload" ] || exit 2
[ "$kind" = ed25519 ] || exit 3
[ "$(wc -c < "$script.sig" | tr -d ' ')" = 64 ] || exit 2
[ "$key" = "$key_id" ] || exit 3
printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "$script.der"
printf '%s' "$pin" | "$REAL_OPENSSL" base64 -d -A >> "$script.der"
"$REAL_OPENSSL" pkeyutl -verify -pubin -keyform DER -inkey "$script.der" \
    -rawin -in "$message" -sigfile "$script.sig" >/dev/null 2>&1 || exit 3
EOF
chmod +x "$test_dir/tools/swift"
installer=$test_dir/swift-install.sh
fixture
run
cmp "$FIXTURE/tmxg_fleet-v1.2.3_darwin_arm64" "$HOME/.local/bin/tmxg"
echo 'PASS: Swift-only valid signature installs (stubbed CryptoKit)'
printf 'tampered manifest\n' >> "$FIXTURE/SHA256SUMS"
refuses
echo 'PASS: Swift-only invalid signature refuses'
fixture
export TEST_TAG=fleet-v9.9.9
refuses
unset TEST_TAG
sed 's/fleet-release-v1/unknown-key/' "$test_dir/good.sig" > "$FIXTURE/SHA256SUMS.sig"
refuses
printf 'fleet-release-v1:ed25519:YQ==\n' > "$FIXTURE/SHA256SUMS.sig"
refuses
printf 'fleet-release-v1:ed25519:YR==\n' > "$FIXTURE/SHA256SUMS.sig"
refuses
echo 'PASS: Swift-only wrong tag, key ID, length and noncanonical base64 refuse'
fixture
printf '#!/bin/sh\nexit 1\n' > "$test_dir/tools/swift"
refuses
echo 'PASS: Swift runtime failure refuses'
rm "$test_dir/tools/swift"
refuses
grep -q 'brew install openssl@3' "$test_dir/output"
echo 'PASS: neither verifier refuses with OpenSSL installation guidance'
installer=$test_dir/install.sh
run --uninstall
test ! -e "$HOME/.local/bin/tmxg"
test ! -e "$HOME/Library/LaunchAgents/ch.blackcode.tmxg.plist"
test -f "$HOME/.local/bin/tmxg.prev-$old_hash"
test "$(cat "$HOME/Library/Application Support/tmxg-trial/config.json")" = 'existing config and state sentinel'
echo 'PASS: uninstall retains data, config and rollback binary'
