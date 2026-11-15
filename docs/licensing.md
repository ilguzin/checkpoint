# Licensing and redistribution

This document describes distribution contents and how third-party licenses are tracked.

The reviewed main LiveKit components (server, JS SDK, React components, and Go SDK) use Apache-2.0. They may be included in commercial distributions subject to the license terms.

Before release:

- Pin dependency versions and distribution contents; prepare a component inventory.
- Include license texts, applicable copyright notices, and NOTICE files.
- Mark modifications to third-party sources if any are made.
- Review transitive dependencies and the license of every component actually shipped.
- Provide `checkpoint licenses` and the Open-source licenses page.
- Checkpoint's own code uses Apache-2.0; see [LICENSE](../LICENSE).

This review of the main components is not a complete license audit of a future build.

## Related documents

- [Releases](releases.md)

## Implemented catalog

`scripts/licenses.py` collects LICENSE/NOTICE/COPYING/COPYRIGHT files for actually linked Go modules and production npm dependencies at pinned versions. `internal/notices/catalog.json` contains 116 entries, including Checkpoint itself; it is embedded in `checkpoint licenses` and `/api/licenses` and exposed through `/licenses`. The native bundle also includes `third_party/` with licenses for the separate LiveKit server and SQLite.

For `@bufbuild/protobuf` 1.10.1, the upstream Apache LICENSE and the BSD notice from the shipped `google/varint.js` are retained because the npm package lacks a separate root LICENSE. Collecting root notices does not establish completeness of all nested notices: public redistribution requires a review of the exact release contents, including the separate official LiveKit binary. Third-party sources are not modified.

## Recording and localization dependencies

The embedded catalog includes media-sdk/ebml-go and MIT-licensed i18next/react-i18next. The optional installer downloads models and llama.cpp from pinned sources with SHA-256 checks and their own LICENSE files: see the [lock file](../deploy/minutes/models.lock.json). Qwen3 uses Apache-2.0; the faster-whisper model/runtime and llama.cpp use MIT. Python wheels are installed with --require-hashes; their licenses are in the installed venv. FFmpeg comes from Ubuntu packages and is not embedded in the Checkpoint binary. Public redistribution of models/venv/FFmpeg requires a separate review of that specific artifact; the current native bundle contains the installer and lock file, not the models.
