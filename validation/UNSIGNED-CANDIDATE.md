# Candidate qualification and release gates

This is a public procedure, not an installation or signing receipt.

Before release, freeze the exact derived source; run native race tests and vet, the supported actual-worker release lane, SDK fixture-only qualification, and package/hash checks. Preserve the known-failing experimental tests separately without treating their failures as release success. Verify the exact published archive and the catalog signature after publication.

Signing must bind the qualified package and manifest hashes. Packaging must preserve the selected default variant; missing or changed defaults must fail the comparison. No source or fixture need be sent to an inference provider for these structural tests.

A plugin is installed only after the host reports the installed and active version. Local qualification, signing, and catalog publication are separate evidence from installation. See `../RELEASING.md` and `README.md`.
