# Contributing

Report bugs and suggest improvements through GitHub Issues. For a substantial change, describe the problem first so the intended behavior can be agreed on before implementation. Small fixes can be submitted directly.

## Checks

Git, Node/npm, Python 3, and Docker are required. Run commands from the repository root:

```bash
./scripts/check.sh
./scripts/installer-smoke.sh  # when changing installation or releases
./scripts/build-native.sh
./scripts/native-smoke.sh    # networking, roles, browser, and native setup
```

The local environment is described in [testing.md](docs/testing.md). Native smoke creates isolated containers; test databases and secrets stay in the ignored `.dev/` directory. WebRTC verification requires actual media streams, not just HTTP health checks. State any unavailable checks in the PR.

## Pull requests

- Keep each PR focused on one task; explain behavior changes and compatibility.
- Add a check that reproduces the bug being fixed when appropriate.
- Update documentation and both RU/EN interface catalogs for user-facing changes.
- Write repository documentation, comments, docstrings, CLI help, and GitHub templates in English. Preserve Russian interface translations and language-specific fixtures used to test supported Russian content.
- Format Go with `gofmt`; `npm --prefix web run build` checks TypeScript and localization.
- Do not add real meeting recordings, passwords, invitations, keys, private target YAML, or internal plans. `.dev/` is not published.

Bug reports should include `checkpoint version`, server OS, browser/device, meeting mode, reproduction steps, and actual results. Review logs and remove secrets, participant names, and meeting content before sharing them.

Report vulnerabilities according to [SECURITY.md](SECURITY.md), not in public issues. Communicate respectfully and stay on topic; harassment, threats, and disclosure of other people's personal information are unacceptable. Contributions are distributed under the project's [Apache-2.0 license](LICENSE).
