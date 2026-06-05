---
name: replay-errors
description: "Analyze a PostHog session replay for errors and build a narrative of what the user was doing when things broke. Use when user pastes a PostHog replay URL, says 'check replay', 'replay errors', 'session errors', or asks 'what errors did this user hit'."
---

# Replay Error Analyzer

Given a PostHog session recording URL, reconstruct what the user was doing and surface every error they hit - both frontend exceptions and API failures.

## Input

A PostHog replay URL. Extract the session ID from the path:
```
https://eu.posthog.com/project/{projectId}/replay/{sessionId}?...
```

The session ID is the UUID segment after `/replay/`.

## Workflow

Follow this exact sequence. Each PostHog MCP call requires `info` before `call`.

### Step 1 - Session metadata

```
posthog:exec({ "command": "info session-recording-get" })
posthog:exec({ "command": "call session-recording-get {\"id\": \"<sessionId>\"}" })
```

Extract: person name, org name, device, OS, browser, location (city/country), duration, start/end time, start URL, console error/warn/log counts.

### Step 2 - All session events

Query every event in the session to build the timeline:

```
posthog:exec({ "command": "info execute-sql" })
```

Then run:

```sql
SELECT
  event,
  timestamp,
  properties.$current_url as url,
  properties.$exception_type as exception_type,
  properties.$exception_message as exception_message
FROM events
WHERE $session_id = '<sessionId>'
  AND timestamp >= '<startDate>'
  AND timestamp < '<endDate>'
ORDER BY timestamp ASC
LIMIT 500
```

Use the recording's `start_time` to derive the date range (same day, or start/end day if it spans midnight).

### Step 3 - Error detail

If `api_error` events exist, fetch their properties:

```sql
SELECT
  timestamp,
  properties.$current_url as page_url,
  properties.method as method,
  properties.status as status,
  properties.message as message,
  properties.path as path,
  properties.endpoint as endpoint,
  properties.error as error
FROM events
WHERE $session_id = '<sessionId>'
  AND event = 'api_error'
  AND timestamp >= '<startDate>'
  AND timestamp < '<endDate>'
ORDER BY timestamp ASC
LIMIT 100
```

If `$exception` events exist, fetch their properties:

```sql
SELECT
  timestamp,
  properties.$current_url as page_url,
  properties.$exception_type as type,
  properties.$exception_message as message,
  properties.$exception_stack_trace_raw as stack_trace
FROM events
WHERE $session_id = '<sessionId>'
  AND event = '$exception'
  AND timestamp >= '<startDate>'
  AND timestamp < '<endDate>'
ORDER BY timestamp ASC
LIMIT 100
```

### Step 4 - Build the narrative

Construct the output from the data collected above.

## Output format

### User context

| Field | Value |
|-------|-------|
| Name | {person name} |
| Organisation | {org name} |
| Device | {OS} / {browser} {version} |
| Location | {city}, {country} |

### Session summary

One paragraph: when the session started, how long it lasted, which pages they visited, and a plain-language summary of what they were trying to accomplish (inferred from the page flow and interactions).

### Error timeline

A chronological narrative connecting user actions to errors. Structure it as:

> **{HH:MM:SS}** - User navigated to `/page`. Clicked {element}. A `{METHOD} {path}` request returned **{status}** - "{message}". They retried {N} times before moving on.

Group rapid-fire duplicates (same endpoint within seconds) rather than listing each one. Include deep links to the replay at error moments using `?t={seconds}` parameter.

### Error summary

| Error | Method | Path | Status | Count | First seen | Last seen |
|-------|--------|------|--------|-------|------------|-----------|

Group by (method, path, status, message). Sort by count descending.

### Suggested investigation

For each distinct error:
- Which backend service likely handles this endpoint (based on path prefix)
- Time window to search logs (first occurrence minus 1 min to last occurrence plus 1 min, UTC)
- The organisation/tenant context if available from person properties

Format the log search suggestion as a ready-to-run command:
```
mise //:logs <service> -- --env production --grep "<path>" --level ERROR
```

## Edge cases

- **No errors found**: Report the session summary and say "No errors detected in this session" with the full event count.
- **Canceled requests** (status 0, message "canceled"): Flag these separately as likely navigation-induced cancellations, not real errors.
- **Paginated events**: If the session has >500 events, run a second query with OFFSET 500 to get the rest.
