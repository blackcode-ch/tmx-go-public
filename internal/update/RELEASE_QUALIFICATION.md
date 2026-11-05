# Release qualification evidence (s22)

`TestSprintS22ReleaseFailureVectors` uses inert bytes, deterministic test-only
signers, a memory HTTP transport, a fake version probe, and a memory rename model.
Staging writes only to a test temporary directory. It does not start the updater
service, listen, dial, invoke a credential helper, or execute a downloaded file.

The vectors cover an accepted signed artifact, missing signature, wrong signing
key, changed signed checksum bytes, and changed artifact bytes. The three rejected
signature cases never fetch the artifact; all rejected cases avoid the probe.
Swap vectors cover Windows executables, macOS bundles, and Unix executables,
including failed installation, successful restoration, and a failed restoration
that retains and reports the old artifact's recovery path.

For later qualification of an actual release, record these fields together:

| Field | Evidence to retain |
| --- | --- |
| source_repository, source_commit | Repository and full immutable source revision |
| source_tree_clean | Whether the build used an unmodified checkout |
| build_workflow, build_run, toolchain | Build recipe revision, immutable run identity, Go version and build flags |
| release_tag, target_os, target_arch | Claimed version and selected platform |
| artifact_name, artifact_sha256 | Exact asset filename and digest of downloaded bytes |
| sums_sha256, signature_sha256 | Digests of the exact SHA256SUMS and detached signature bytes |
| trusted_key_fingerprint | SHA-256 of the trusted Ed25519 public key; never private key material |
| probe_result, swap_result, recovery_path | Qualification outcome and recovery evidence, with personal paths redacted |

The existing signature binds the checksum file to a trusted signer, and the
checksum binds the selected artifact filename to its bytes. It does **not** bind
those bytes to a source commit or build run. Retaining the source/build fields is
an evidence requirement for later qualification, not a new attestation format or
a claim that these fixture tests establish reproducible builds or release safety.

Run the deterministic acceptance case with:

    go test ./internal/update -run '^TestSprintS22ReleaseFailureVectors$' -count=1 -v
