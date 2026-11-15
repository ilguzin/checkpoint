# Reporting vulnerabilities

The preferred channel is [GitHub private vulnerability reporting](https://github.com/ilguzin/checkpoint/security/advisories/new). If the Report a vulnerability button is unavailable, open an issue asking for a private reporting channel **without exploit details, secrets, or meeting data**.

Include the affected version, prerequisites, impact, and a minimal reproduction. Do not attach live passwords, API keys, invitation links, or recordings of other people. Coordinate disclosure after a fix. No fixed response time is promised yet.

Relevant issues include authentication or role bypass, access to another room's history, unauthorized recording, code execution through messages or minutes, workers escaping job boundaries, privilege escalation, and installation of unsigned artifacts. Insecure third-party component configuration introduced by Checkpoint is also in scope.

Fixes currently target the current version. Pre-alpha status does not imply production readiness. Access-model limitations are documented in [access.md](docs/access.md); signatures and trust recovery are covered in [releases.md](docs/releases.md).
