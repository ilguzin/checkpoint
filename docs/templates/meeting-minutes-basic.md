---
template_id: basic
template_version: 1
title: "{{meeting_title}}"
room_id: "{{room_id}}"
meeting_occurrence_id: "{{meeting_occurrence_id}}"
recording_run_id: "{{recording_run_id}}"
date: "{{meeting_date}}"
timezone: "{{meeting_timezone}}"
recording_started_at: "{{recording_started_at}}"
recording_ended_at: "{{recording_ended_at}}"
language: "{{output_language}}"
status: final
review_status: unreviewed
generated_by: ai
participants: []
decisions: []
action_items: []
source_transcript_id: "{{transcript_id}}"
---

# {{meeting_title}}

AI-generated · Not reviewed

## Meeting

Date, time zone, recording period, participants, and their roles. Indicate whether only part of the meeting was recorded.

## Summary

Three to five substantive points supported by the recording. Do not invent missing information.

## Context and goals

Separate the predefined agenda/goals from context inferred from the discussion. State when no agenda or goals were provided.

## Discussion

Group by topic. Link to timestamps in the saved recording and reflect disagreements.

## Decisions

| Decision | Rationale / conditions | Source |
|---|---|---|

Distinguish accepted decisions from proposals. Explicitly state when no decisions were recorded.

## Action items

| Action | Owner | Due date | Source |
|---|---|---|---|

Use “Not specified” for unknown owners or due dates. Do not infer them from the participant list.

## Open questions

Unresolved questions, next steps requiring clarification, and ambiguous assignments.

## Recording quality and sources

Links to the recording and transcript. Gaps, overlapping or unintelligible speech, shared microphones,
unknown speakers, and attribution limitations. Template and model version metadata.

<!-- This is a specification template, not the document-generation implementation.
Headings and content are generated in output_language; metadata keys remain stable.
Replace instructions with supported content, not guesses.
Source links use start_ms/end_ms relative to the recording and authorized routes.
The final status means generation is complete; review_status tracks a separate optional human review.

The basic@1 template supports English and Russian output. Localize every section heading,
table heading, explanatory/generated passage, the AI-generated review label, and the
placeholder for unspecified values according to output_language.
Metadata keys and enum values remain unchanged; preserve participant names and source links.
-->
