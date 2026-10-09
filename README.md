# id.aiii.codequality

An AII OS plugin that measures source code quality with TypeSafe's Jev as the
judge. It asks a fixed catalogue of fault statements about each file's code and,
separately, its comments, and scores the answers into findings, dimension
scores, an index and a band. It judges texts passed in, or scans a folder in the
identity's sandbox a few files per call. It needs AII OS 0.1.10 or newer and a
key for Jev, which the host keeps and grants for `api.typesafe.ai:443` only.

Build the WebAssembly guest with TinyGo 0.42 or newer:

```sh
./build.sh              # dist/id.aiii.codequality.wasm; TINYGO names TinyGo when it is not on PATH
go tool aiisdk build    # the same build, through the plugin SDK go.mod names
```

Copyright (c) AI Identity Incorporated. Licensed under Apache-2.0; see LICENSE.
The language table in `cq/annex_l.json` is derived from third-party data; see NOTICE.
