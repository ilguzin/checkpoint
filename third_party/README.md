# Third-party dependencies

Versions are pinned in [`go.mod`](../go.mod), [`go.sum`](../go.sum), [`web/package-lock.json`](../web/package-lock.json), [`Dockerfile`](../Dockerfile), and the Compose profile. The embedded [catalog](../internal/notices/catalog.json) contains license and notice texts for linked Go modules and production frontend dependencies; users can view it through `checkpoint licenses` and `/licenses`. The table below lists selected main components, not the complete dependency inventory.

| Component | Version | Upstream license |
|---|---|---|
| LiveKit Server | 1.13.11, image digest and native archive SHA-256 pinned | Apache-2.0 |
| LiveKit Go SDK | 2.19.0 | Apache-2.0 |
| LiveKit protocol | 1.51.1-0.20260910121219-271d9cde3897 | Apache-2.0 |
| LiveKit JS client | 2.23.0 | Apache-2.0 |
| LiveKit React components | 2.9.25 | Apache-2.0 |
| LiveKit component styles | 1.2.0 | Apache-2.0 |
| React / React DOM | 19.3.1 | MIT |
| tslib | 2.8.1 | 0BSD |

Original license texts from these versions are preserved in [licenses](licenses). No third-party source modifications are included. Checkpoint's own code uses [Apache-2.0](../LICENSE). Transitive Go/JS packages and base images still require a complete distribution review before a public release; this table does not claim that review is complete.

Build/test tools: Go 1.27.3, Node 26.10.0, TypeScript 7.0.2, Vite 8.3.7, React plugin 6.3.0, Playwright 1.65.0. Node and Go builder images are pinned by digest. The local development application uses a scratch image with the Go binary and CA certificates. Native VPS installations run the binary directly and do not require Docker.

Primary sources: [LiveKit Server](https://github.com/livekit/livekit/tree/v1.13.11), [Go SDK](https://github.com/livekit/server-sdk-go/tree/v2.19.0), [JS SDK](https://github.com/livekit/client-sdk-js), [React components](https://github.com/livekit/components-js), [React](https://github.com/facebook/react), [tslib](https://github.com/microsoft/tslib).

SQLite: `modernc.org/sqlite v1.62.0` (BSD-3-Clause), license text in [licenses/sqlite-LICENSE](licenses/sqlite-LICENSE). Review the complete transitive dependency catalog for each distribution.

## Updating the inventory

With frontend dependencies installed and Docker available, run `python3 scripts/licenses.py` from the repository root. Review the generated catalog diff against the pinned dependency versions. The native bundle includes this directory and the embedded catalog. Optional recording models, Python packages, and FFmpeg are installed separately; see [licensing and redistribution](../docs/licensing.md) for their distribution boundary.
