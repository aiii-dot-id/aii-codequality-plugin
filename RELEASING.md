# Releasing

A release is a signed package attached to a GitHub release of this repository, and an entry in
the AII OS plugin catalog.

1. Bump `version` in `plugin.json` and add the entry to `CHANGELOG.md`.
2. Prove the tree:

   ```sh
   go test -race ./...
   go tool aiisdk build
   go tool aiisdk package
   go tool aiisdk test -grant files=$PWD/testdata/tree -grant net.outbound:api.typesafe.ai:443
   ```

   Run the last step against the AII OS release named by `AII_OS_BIN`.
3. Record the package hash and manifest hash that `aiisdk package` prints. The package is
   canonical: the same tree yields the same bytes.
4. Signing. The package is signed at the platform tier, T3, by the AIII platform authority, in
   the ceremony that also signs AII OS releases. The keys never leave that ceremony and are no
   part of this repository. The plugin attaches the operator's stored Jev key to a public host,
   which the host permits only for a review-proven package (T2 or T3).

   Stage the packaged tree for the signer and emit the payload to sign. `aii-devsign` is built
   from the AII OS source with `go build ./cmd/aii-devsign`.

   ```sh
   go run ./cmd/stage-t3
   aii-devsign -staging dist/t3 -payload-out dist/t3/ceremony-payload.json
   ```

   The payload's `package_hash` equals the one `aiisdk package` printed. Its `manifest_hash` is
   that of the manifest `aii-devsign` writes, which differs from the SDK's only by the absent
   `default_variant`.

   The ceremony signs the payload as `plugin.platform_release`. Assemble and self-verify with:

   ```sh
   aii-devsign -staging dist/t3 -attach-sig platform.sig.json -root <platform root> -status <snapshot> -o <pkg>
   ```

   The signed `.aiiospkg` must verify offline with `aii plugin verify -platform-key <root>
   -trust-dir <trust>` before anything is published.
5. Publish a GitHub release tagged `v<version>` with the signed `.aiiospkg` as its asset.
6. `go tool aiisdk publish` prints the catalog entry: the package URL, its hash and its size.
   Submit it to `aiii-dot-id/plugin-catalog`.

Releases are immutable. A mistake gets a new version.
