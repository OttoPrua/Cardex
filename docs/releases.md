# Release publication

The version is `const version` in `cmd/cardex/main.go`. Update it on a reviewed PR,
then merge after the Linux, macOS and Windows Validate jobs succeed.

Create a new `v<version>` tag at the exact current `main` commit and push the tag.
The existing Validate workflow checks all three platforms again. Its release job
requires the tag version to match the source and the tag commit to equal remote
`main`; it refuses to overwrite an existing release.

`python3 scripts/build-release.py` builds the six supported archives and copies the
two installers. `python3 scripts/verify-release.py <version> <commit>` verifies
all eight payload checksums, archive layout, clean VCS identity and the native
binary's version. The workflow then publishes these eight files plus `SHA256SUMS`.
The release becomes the latest stable release used by the existing installers.
Publication does not install Cardex or start services on any user device.

Cross-compilation verifies buildability, not native execution. Validate runs the
full Go suite on Linux/macOS and the selected Windows lifecycle/onboarding tests;
Windows ARM64 and the other non-runner architectures remain build-only coverage.
`make accept-sync` is separate installed-machine acceptance and needs the external
sync/fingerprint helpers and installed design-review template. Missing helpers or
skipped tests do not certify those installed guardrails.
