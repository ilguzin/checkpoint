# Recording and meeting minutes

[Architecture](architecture.md) · [Basic template](templates/meeting-minutes-basic.md)

The module records microphone audio and optionally generates a transcript and meeting minutes. Video and screen-share audio are outside the supported profile. Models and services are installed separately from the base application.

## Recording flow

Owners and Room Admins can start recording. At start, they choose whether to generate minutes afterward: Yes saves that intent; No keeps the recording only. Every start creates a separate recording period; every stop completes it, saves the combined audio file, and queues processing if minutes were requested. Jobs do not process unfinished files before recording actually stops.

Participants see a recording indicator. Its hover/focus tooltip distinguishes recording only from recording with minutes generation. In the header, the indicator is combined with room history. The start button is green; during recording it is red with a recording dot. If the recorder is unavailable, Owners/Admins see a disabled start action with an explanation; unavailable processing is not offered as a ready feature.

History shows recording and processing states, including queued, running, failed, partial, and cancelled. Owners/Admins can cancel processing with confirmation, retry explicitly, request minutes for a saved recording, edit the result, and mark a revision as reviewed. Cancellation does not remove the combined mix or previously saved documents. Late results from a cancelled job are rejected.

## Preventing indefinite recording

The default recording window is two hours. Five minutes before it ends, Owners/Admins are asked to extend it. If nobody confirms, recording stops and any requested minutes enter processing. An extension takes effect after recorder acknowledgment, not merely a browser click.

An empty room triggers automatic stop after 60 seconds. Silence while participants are present does not count as an empty room. The recorder's service connection must not keep recording alive. The operator configures timers through the CLI profile.

Recording does not silently resume after a failure: available files are finalized as partial. Recorder and processor use leases and heartbeats; cancellation, an expired job lease, or room deletion prevent acceptance of late artifacts. A lease lasts 30 seconds, heartbeats run every two seconds, and the watchdog stops a worker after 12 seconds without acknowledgment.

## Individual tracks and combined mix

The recorder saves separate temporary WebM sources. Metadata associates each source with participant identity, account if available, display name at subscription time, track SID, and offset from recording start. Matching display names do not merge sources. A shared microphone cannot reliably distinguish which person spoke into it.

At finalization, FFmpeg creates a combined AAC/M4A mix from temporary sources using their offsets. **This is not a second, independent real-time capture.** Overlapping speech is preserved; ASR timestamp accuracy is not the same as audio-container timing accuracy.

For minutes, ASR processes sources separately, then merges results onto a common timeline with participants and timestamps. After the transcript and mix are durably saved and verified, individual tracks, including normalized copies, are deleted. For recording-only sessions, they are deleted after the mix is confirmed; failures are governed by the temporary-audio TTL.

History retains the combined mix, transcript, and minutes. If minutes are requested later after individual audio has been deleted, processing uses the mix: exact per-person attribution cannot be recovered. A combined file must not be presented as an exact transcript of individual participants.

## Access and room history

Recordings and documents belong to a room and a specific meeting occurrence. Room access grants access to all retained history, including material from before the first visit. History can be opened without joining the media session.

Once a signed-in user is successfully admitted by ID/invitation, the room appears under **Shared with me**. Opening a link or waiting in the lobby does not create membership. Anonymous guests use a limited archive session; a persistent cross-device list requires an account. A matching display name does not link a guest to someone else's account.

Access is checked on every list, document, download, and HTTP Range request. Closing a room blocks participant and guest archive access; the Owner retains access until deletion. A revoked invitation, disabled account, or expired session cannot grant access through an old file link. An ordinary ID remains an independent guest-admission secret under the room's current policy. See the [access model](access.md).

## Deleting a room and all its content

An owner can delete their own room, including a closed room, after confirmation. Room Admins and ordinary participants cannot. Closing preserves the archive; deletion removes the room and its content.

Deletion ends the active meeting, stops recording, cancels jobs, and revokes invitations/sessions. It covers the combined mix, temporary tracks, transcripts, minutes revisions, and memberships. An independent deletion ledger prevents deleted material from returning when an old backup is restored. Cleanup failures require investigation; inaccessible files must not be treated as permission to access them.

Backups have their own retention periods: deletion from the live archive does not immediately erase previously created external copies. Restore requires the current deletion ledger and verification of restored paths/permissions: see [operations](operations.md#recording-setup-and-management).

## Basic template and completed minutes

The built-in `basic@1` template includes topic, date, participants, recording period, summary, context, discussion, decisions, tasks with assignees and deadlines, open questions, and source quality. See the [template](templates/meeting-minutes-basic.md).

The generator returns structured output with segment references. Format, permitted references, and timestamps are validated; unknown assignees and deadlines must not be invented. This validation does not establish LLM semantic accuracy. Results are labeled AI-generated and unreviewed; Owners/Admins can edit the text and save a reviewed revision. Markdown does not execute HTML or commands from transcripts.

## Configuration

The main operator interface is `checkpoint minutes`; room Owner/Admin rights do not grant access to server keys or models. The CLI and `spec.minutes` in target YAML share a schema. See the [example profile](../deploy/minutes/minutes.example.yaml) and [setup and commands](operations.md#recording-setup-and-management).

| Setting | Default |
|---|---|
| Minutes `language` | `ru`; `en` is also supported |
| `confirmationSeconds` / `warningSeconds` | 7200 / 300 seconds |
| `emptySeconds` | 60 seconds |
| `maxConcurrentRecordings` / `maxSources` | 2 recordings / 32 sources |
| `minFreeBytes` / `maxStorageBytes` | 2 GiB reserve / 20 GiB archive |
| `failedAudioTTLSeconds` | 7 days |
| `transcription` | faster-whisper medium, CPU/int8, two threads, automatic language |
| `generator` | local llama.cpp / Qwen3-4B-Q4_K_M |

ASR and generation can be switched independently through supported adapters. An operator explicitly enables an API profile with endpoint, model, revision, and a private credential-file reference. Keys are not stored as values in YAML, the browser, or Git. There is no automatic cloud fallback. Profile changes apply to new recordings; existing jobs retain their original settings.

Exact versions, SHA-256 hashes, and licenses are pinned in [models.lock.json](../deploy/minutes/models.lock.json). Local models require additional memory and CPU; protective quotas are not capacity or performance promises. Use `checkpoint minutes config show` for the complete set of current settings.

## English and Russian

Recognition supports English and Russian, including automatic detection within speech segments. Transcripts preserve the original language; the output language for minutes is configured separately through the profile's `language` field. Users select interface language in settings; this does not translate existing documents.

Names, terminology, numbers, negation, and overlapping speech may be recognized incorrectly. Long meetings are processed in chunks; structural reference validation does not replace human review of the content.

## Setup, storage, and observability

Core owns SQLite and the archive; recorder and processor run under separate UIDs with private temporary directories and no direct database access. They exchange data through a restricted Unix API. The local generator listens on loopback and requires its own key. An invalid profile or unavailable worker does not stop ordinary calls.

Backups include SQLite, the combined mix, and manifests, but exclude individual audio. Models and runtimes are not included in every archive. Full recovery also requires the current deletion ledger, configuration, certificates, and correct UIDs/paths.

Public Observer exposes module readiness and queue state without room names, participant names, minutes text, or private URLs. Detailed diagnostics are available to operators through the CLI. Measurements from short local recordings do not establish long-meeting processing speed or the ability to sustain maximum call load concurrently.
