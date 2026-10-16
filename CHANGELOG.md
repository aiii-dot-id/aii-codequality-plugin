# Changelog

## 0.1.13

- Keep a pending multi-chunk file when a retry uses a smaller call budget. Cached answers continue across bounded steps; the file-size limit is unchanged.

## 0.1.12

- Repackage with the corrected SDK so the declared dynamic model picker survives into the signed settings member.

## 0.1.11

- Model discovery refuses malformed entries, duplicate JSON keys, and more than 256 distinct models. It no longer silently skips bad entries or trims a list while reporting success. Duplicate valid model names still appear once.

## Earlier releases

- Preserve raw-string boundaries when locating findings within source units.
- Report each scan step's current error and resume pending work from cached answers.
- Support nested report globs and count the records actually returned.
- Distinguish findings from leads using the fixed catalogue calibration table.
- Carry complete findings, fault descriptions and named failure reasons to callers.
- Bound Jev calls and distinguish transient errors, declined text and refused keys.
- Resolve scanned paths through the host sandbox and keep credentials in host storage.
