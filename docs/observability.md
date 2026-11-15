# Public service health website

This document describes the system contract and operational limitations. Target metrics are not an SLA guarantee.

The site is accessible to everyone without signing in. A public installation uses a separate domain, `status.example.com`, with its own navigation, responsive interface, and pages. The main application includes a visible “Service health” link, including beside connection errors.

The interface name is **Checkpoint Service Health**. Its design follows Checkpoint's visual style, with clear labels, accessible colors, and textual status explanations. Visitors should not need to understand process names, ICE, CPU, or HTTP codes to know whether they can join a meeting.

## Implemented as of 2026-11-10

A separate public UI and SQLite database support Overview, Connection quality, History, and Connection check. Core HTTP and the authenticated LiveKit API are polled every 5 seconds. Outage and recovery require two consecutive confirmations; the first successful measurement permits Operational status. Data older than 15 seconds becomes Unknown. Maintenance declared through the CLI or updater appears as Maintenance.

History is retained for 30 days, with at most the 100 latest incidents returned. Daily availability = Operational / (Operational + Outage), requiring at least 12 known measurements; collected versus expected 5-second samples are shown separately. Unknown and Maintenance are excluded from the denominator. No measurements means no percentage; gaps do not count as successful operation. This is observed local availability, not guaranteed external uptime.

Quality shows the p95 of local HTTP/API probes over 24 hours when the sample is sufficient. Overall media quality remains Unknown. Connection check starts a real WebRTC receiver and verifies at least 50 RTP audio packets from a short-lived synthetic source; it does not use the visitor's microphone or camera. Packet counts, loss, and jitter are shown for this check. Rate limits and a maximum of four simultaneous sources bound the public workload.

The target specification follows below. Continuous end-to-end media measurements, a scheduled maintenance editor, manual incident updates/root causes, and fleet-wide user telemetry are **not yet implemented**. History records actual automatic transitions without inventing explanations. A complete VPS outage requires an external observer.

## Pages and user questions

| Page | Content | Question answered |
|---|---|---|
| Overview | Overall status, current incident/maintenance, last check time, and the state of sign-in, calls, webinars, and screen sharing | “Is the service working? What is affected?” |
| Connection quality | Measured connection time, latency and loss, trends, sample size, and source | “Is the connection worse across the service or just for me?” |
| History | Incident and maintenance timeline, cards with causes and progress, and daily availability over 30 days | “What happened and how was it resolved?” |
| Connection check | Visitor-initiated HTTPS, signaling, and test media connection checks with practical advice | “Can I connect from this device and network?” |

Each component card shows its status, a short explanation of the impact on users, and data freshness. Incident pages include the start time, affected features, updates, recovery, and a final explanation. Scheduled maintenance is announced in advance with a time zone and expected impact.

States are Operational, Degraded, Outage, Maintenance, and Unknown. They include plain-language descriptions such as “Ready to join,” “Joining may take longer,” “New meetings are temporarily unavailable,” or “No recent data.”

## Data integrity

- Every metric has a timestamp, source, and scope: local server check, synthetic media test, aggregated client measurements, or external check.
- Missing measurements do not imply health. Stale checks become Unknown; the last known state is displayed separately with a timestamp.
- A successful HTTP health check does not prove that audio, video, or screen sharing works. Untested features do not automatically receive a green status.
- The quality section shows sample size. With insufficient data, it displays “Not enough data” instead of an invented quality or availability percentage.
- Availability is calculated over defined observation intervals; gaps and maintenance are visible separately. Define the formula and maintenance exclusion rules before implementation.
- One failed check must not cause endless status flapping: consecutive failure/success thresholds and freshness limits are explicit and tested.
- Web application failure and media server failure are distinct. The site must be able to report “Joining new meetings is unavailable; existing calls may continue.”
- Synthetic checks use a separate private test room, minimal permissions, and a limited frequency. They do not join real meetings.

## Privacy and administration

Only predefined statuses, safe aggregates, and reviewed incident descriptions are public. Participant names/counts for individual rooms, room names, messages, IP addresses, exact internal addresses, raw logs, stack traces, tokens, and server secrets are not published. Public aggregates cannot be filtered down to a specific participant or meeting.

Technical details, host resource metrics, diagnostic exports, and incident changes require authorized administrator access. Failure of administrative authentication blocks administrative actions while leaving the public overview available. Management endpoints do not become public alongside read-only pages.

A visitor's connection check runs only at their request. The basic synthetic test does not record conversations or request camera/microphone access. Individual visitor results do not enter public history. Public test-token issuance is constrained by rate limits, a short TTL, and a server-controlled test room; callers cannot request arbitrary rooms or permissions.

## Deployment and limitations

- Observer runs independently of Checkpoint and stores history in a separate local database. It is a separate container in the Docker profile and a separate systemd service in native installations.
- In the tailnet, public access means no application login; network access remains limited to the tailnet. Exposing the environment to the internet through Funnel is not required.
- The tailnet site uses a separate available HTTPS port on the existing `*.ts.net` name; an arbitrary `status.<node>.ts.net` name is not assumed. A public VPS uses a separate DNS hostname.
- Core and Observer updates are separate steps: Observer displays application maintenance, while its own restart remains a brief observation gap and is not disguised as a healthy state.
- On the same VPS, Observer survives a Checkpoint process failure but not an outage of the VPS, Nginx, or network. This distribution does not claim to provide an independent external status platform.
- Reporting a complete VPS outage requires an external probe and a site or mirror hosted elsewhere. The measurement source location is always visible; a bundled Observer on the same VPS is not external monitoring.

## Site acceptance criteria

- Overview and history open without an account at the advertised address.
- The separate site continues responding when Checkpoint core is stopped and reports its unavailability after the configured number of checks.
- A stopped LiveKit service, signaling errors, degraded media tests, and stale measurements produce distinct, understandable states.
- The UI contains no fabricated charts or “100% uptime” before measurements accumulate.
- History survives Observer restarts, and observation gaps remain visible.
- Tests verify that the public API exposes no private data and administrative actions require authentication.
- Main pages work well on phones, support keyboard navigation, and explain status with text as well as color.

## Related documents

- [Architecture and reliability](architecture.md)
