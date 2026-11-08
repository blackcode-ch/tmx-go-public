# Install tmxg

[fleet-v1.0.1](https://github.com/blackcode-ch/tmx-go-public/releases/tag/fleet-v1.0.1)
provides headless binaries with the web UI embedded. They run the daemon and CLI;
they are not desktop application bundles. Download only from the public release.

| System | Asset |
| --- | --- |
| Windows x64 | `tmxg_fleet-v1.0.1_windows_amd64.exe` |
| macOS Apple Silicon | `tmxg_fleet-v1.0.1_darwin_arm64` |
| macOS Intel | `tmxg_fleet-v1.0.1_darwin_amd64` |
| Linux x64 | `tmxg_fleet-v1.0.1_linux_amd64` |

## macOS installer

Run as your normal user, without `sudo`. Signature verification requires either
OpenSSL 3 (for example, `brew install openssl@3`) or Swift with CryptoKit from
Xcode Command Line Tools (`xcode-select --install`). Finish installing that
prerequisite before running:

```sh
curl -fsSL https://raw.githubusercontent.com/blackcode-ch/tmx-go-public/main/scripts/install-macos.sh | bash
```

The installer selects the newest published stable fleet release, rather than
pinning a version. It chooses the Mac's native architecture, verifies the signed
checksum manifest and binary, installs `~/.local/bin/tmxg`, and starts a user
LaunchAgent. It retains an existing executable for rollback and does not erase
your state. It refuses a custom `TMXG_HOME`; that needs a separately configured
LaunchAgent.

Print the private local UI URL after installation, then open it in your browser:

```sh
"$HOME/.local/bin/tmxg" open
```

## Verify a manual download

Do this before running any downloaded binary. A checksum alone does not prove
who published it: verify `SHA256SUMS.sig` as well. The repository's
`cmd/public-release` verification tool checks the Ed25519 signature against
pinned public keys, binds it to the public repository and exact tag, and checks
all four binary hashes against the exact manifest bytes.

On a trusted verification computer, install Git, the GitHub CLI (`gh`), and the
Go toolchain required by `go.mod`. Obtain a reviewed checkout of
`blackcode-ch/tmx-go-public` and independently confirm the release signing public
key through a trusted channel. Do not take a new key solely from the release
you are about to verify. Create `public-pins.json` in that checkout, replacing
the placeholder with the verified **public** key:

```json
{
  "fleet_keys": [
    {"id": "fleet-release-v1", "type": "ed25519", "key": "<verified base64 public key>"}
  ],
  "require": ["ed25519"]
}
```

Never put a private signing key in this file. In the checkout directory, use a
new, empty `release-dist` directory and run these commands in PowerShell or Bash:

```sh
gh release download fleet-v1.0.1 --repo blackcode-ch/tmx-go-public --dir release-dist
go run ./cmd/public-release -mode verify -tag fleet-v1.0.1 -dir release-dist -sig release-dist/SHA256SUMS.sig -pins public-pins.json
```

Keep all six assets in `release-dist`: four binaries, `SHA256SUMS` and
`SHA256SUMS.sig`. The verifier requires all four platforms even if you only
intend to run one. Continue only if it exits successfully. Missing assets,
unknown keys, invalid signatures or mismatched checksums are failures; do not
bypass them. Verification can happen on another trusted computer, but ensure
the installed bytes still match the verified manifest after transferring them.

## Windows x64

After successful verification, run from the checkout in PowerShell:

```powershell
Copy-Item release-dist/tmxg_fleet-v1.0.1_windows_amd64.exe ./tmxg.exe
.\tmxg.exe version
.\tmxg.exe up
.\tmxg.exe open
```

Keep the executable in a permanent user-owned directory if you move it later.
`open` prints the local UI URL. Open it in your browser and keep it private:
it includes your owner credential.

## Linux x64

After successful verification, run from the checkout:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 release-dist/tmxg_fleet-v1.0.1_linux_amd64 "$HOME/.local/bin/tmxg"
"$HOME/.local/bin/tmxg" version
"$HOME/.local/bin/tmxg" up
"$HOME/.local/bin/tmxg" open
```

Use `open` on a machine with a browser. For access from another device, first
configure trusted HTTPS access and follow [phone pairing](PHONE-PAIRING.md).
Do not expose the local API directly. Add `~/.local/bin` to your shell's `PATH`
if you want to type just `tmxg`.

## State, features and updates

The default profile is `trial`. State lives in `%LOCALAPPDATA%/tmxg-trial` on
Windows, `~/Library/Application Support/tmxg-trial` on macOS, and
`~/.local/state/tmxg-trial` on Linux. `TMXG_HOME` overrides that location.
The local API defaults to port 8797; `TMXG_PORT` overrides it.

Production supports optional P2P chats and files, Companion and WhatsApp;
these channels default off, and the trial profile forces them off. See the
[agent skill](../internal/skill/SKILL.md) for the per-feature defaults.

Automatic updates are forced off on config load. Signed manual updates require
owner-protected setup of the public release repository and independently
verified fleet key pins. The default update repository is still the original
upstream repository, so merely downloading a public binary does not configure
its updater. Keep your previous executable when migrating an existing install.
