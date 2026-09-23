# Changelog

## 0.1.2

- **`scan` works in the identity's sandbox.** The plugin no longer has folders of its own. With
  files granted on its card, `scan` takes one `path`, named as the identity's own tools name
  it: relative to its home, or absolute inside a folder added in Settings → Sandbox. The
  `root` argument is gone.
- **Capability `fs.sandbox`** replaces `fs.roots`. **Needs AII OS 0.1.10 or newer**
  (`aiios_min_version`): an older host knows no sandbox for plugins, so it is not offered this
  release.

## 0.1.1

- **The key is pasted on the plugin's card.** The `api_key` setting is still a credential handle
  the plugin never sees. On AII OS 0.1.9 the card takes the key itself, keeps it privately,
  and grants it to this plugin for `api.typesafe.ai` only.
- **The model is chosen from Jev's own list.** The `model` setting names a new read operation,
  `models`, which asks Jev (`GET /v1/models`) with the pasted key. The card offers the answer
  as a drop-down; if the lookup fails, it keeps a text field and says why.
- **The default model is `jev-latest`.** That is the name Jev lists; `jev-1.13.0` was not in
  the list.
- **Needs AII OS 0.1.9 or newer** (`aiios_min_version`). An older host reads the settings
  strictly and would refuse this release, so older hosts are not offered it.

## 0.1.0

First release.

- `judge`, `scan` and `report` over TypeSafe's Jev (`/v1/systemone`).
- Before any text leaves the machine, comments are blanked (line numbers kept) and secrets
  redacted. Code and comments are asked in separate calls.
- Catalogue `general.v3`:
  - 16 general fault statements
  - 4 per language for Go, Python, JavaScript/TypeScript and Java
  - 5 about comments
- Scoring: findings at p ≥ 0.6; low / medium / high deduct 5 / 12 / 25 from their dimension; the
  index is the mean of the dimensions present; bands are clean, minor, moderate and severe.
- Answers are cached by content, catalogue and model, so an unchanged tree re-scans without a
  call.
- The scan is resumable, six Jev calls per step. It follows the language table, `.gitignore`,
  `.gitattributes` and the caller's excludes. It skips nested repositories, symlinks,
  secret-named, binary, generated, minified, oversize and test files, and counts every skip by
  reason.
- A text Jev's hosted edge refuses is recorded under `refused`, and the scan moves on.
