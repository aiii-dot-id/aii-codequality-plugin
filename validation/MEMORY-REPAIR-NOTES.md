# Memory investigation scope

Expanded 1 MiB support is deferred. The retained experimental guest tests reproduce workloads that exhausted a 64 MiB worker. Diagnostic experiments observed fragmentation, allocator growth constraints, and conservative retention through false pointer matches. Instrumented measurements do not establish production behavior or a safe runtime repair.

The supported release uses the standard toolchain, a 128 KiB default and 192 KiB maximum, and the unchanged sustained-workload assertions. A passing bounded workload is not proof of an indefinite memory plateau. No experimental runtime patch is part of this release.

See `guest/README.md` for both lanes. Private diagnostic chronology and deployment identifiers are intentionally not reproduced here; executable regressions are retained.

`memory_edit.py` is a retained historical source-mutating migration script, not an acceptance test or a current repair. Its literal targets describe older source; do not run it on the release tree.
