# Email Notifications (Track)

Track can email people when something happens on an issue or sprint they care
about. The whole feature is **dark by default**: with `EMAIL_ENABLED` unset,
Track behaves byte-for-byte as it did before — no worker starts, no rows are
written, no addresses are read.

This document covers the events, defaults, configuration, delivery model, and
security posture. It is the operator-facing reference for turning the feature
on.

---

## Events

| Event                     | `event_type` key        | Who is notified (actor always excluded)                |
|---------------------------|-------------------------|--------------------------------------------------------|
| Issue assigned to you     | `issue.assigned`        | The assignee                                           |
| You were @mentioned       | `issue.mentioned`       | Each resolved mentioned member                         |
| Comment on your issue     | `issue.commented`       | Issue participants (creator, assignee, past commenters)|
| Status change on an issue | `issue.status_changed`  | Issue creator + participants                            |
| Sprint started            | `sprint.started`        | Sprint members (distinct assignees of issues in cycle) |
| Sprint ended              | `sprint.ended`          | Sprint members                                         |

The person who **performed** the action is never emailed about their own action
(`dedupeExclude` in the dispatcher). Recipients are de-duplicated per event.

### Watchers

Track has no explicit "watch" table. Participation **is** the watch signal: the
creator, the current assignee, and anyone who has commented are an issue's
implicit watchers (`IssueParticipants`).

---

## Preferences & defaults

Preferences are stored server-side per member, per event type
(`notification_preferences`). The model is **opt-out**:

- **No row → the member receives the email** (default ON).
- A row with `email_enabled = false` suppresses that event type for that member.

Members manage their own preferences; the footer of every email links to the
preferences page.

> ⚠️ **Divergence from the original spec — read before flipping on.** The
> overnight spec called for a *default-OFF* posture for everything except
> mentions + assignments ("conservative against spam"). This implementation
> ships **default-ON for all event types** (opt-out), a deliberate, test-pinned
> choice in the original PR (`migrations/0017`, `preferences.go`). Before
> enabling email for real customers, decide which default you want — default-ON
> means a busy workspace will email participants on every comment/status change
> until they opt out. See the filed issue for the conservative alternative.

---

## Configuration

Email is delivered over **SMTP only** (no third-party email API dependency).
SMTP settings live in **product-neutral `EMAIL_*` env vars** because the
`internal/email` package is shared across Talyvor products (Track, Docs, …).
Per-product config (base URL for deep links) stays under `TRACK_*`.

| Variable           | Required (when enabled) | Default     | Notes                                   |
|--------------------|-------------------------|-------------|-----------------------------------------|
| `EMAIL_ENABLED`    | —                       | `false`     | Master switch. Off ⇒ feature fully dark.|
| `EMAIL_SMTP_HOST`  | ✅                      | —           | SMTP relay host                          |
| `EMAIL_SMTP_PORT`  |                         | `587`       | STARTTLS negotiated when advertised      |
| `EMAIL_SMTP_USER`  |                         | —           | If empty, no SMTP auth is attempted      |
| `EMAIL_SMTP_PASS`  |                         | —           | **Never logged**                         |
| `EMAIL_FROM`       | ✅                      | —           | Envelope + From address                  |
| `EMAIL_FROM_NAME`  |                         | `Talyvor`   | Display name                             |
| `TRACK_APP_BASE_URL`|                        | `localhost` | Used to build deep links in emails       |

### Enabled-but-misconfigured behaviour

If `EMAIL_ENABLED=true` but the minimum SMTP settings (`HOST` + `FROM`) are
absent, Track **falls back to a no-op sender** (logs, sends nothing) rather than
failing startup. This is a deliberate fail-safe in the original PR
(`sender_test.go`).

> The overnight spec asked for the opposite — *fail startup with a clear error*
> so a half-configured deployment is loud, not silently dark. Both are
> defensible; the divergence is filed as an issue for a decision. Today the
> behaviour is fail-safe (never crashes, never sends from a broken config).

---

## Delivery model

```
event seam (issue/cycle handler)
   └─ Dispatcher: resolve recipients → exclude actor → dedupe
        → preference filter → load addresses (from DB, by member ID)
        → render once (HTML + text)
        → Queue.Enqueue  ── never blocks the request ──┐
                                                        │
   in-process worker pool ◀──────────────────────────────┘
        → SMTP send, bounded retry with backoff
        → on success: done
        → on exhausting all attempts: **dead-letter** (durable) + log
```

- **Never blocks a request.** `Enqueue` is non-blocking; if the bounded buffer
  is full it drops with a warning (best-effort).
- **Retry/backoff.** Each message is attempted `Attempts` times (default 3) with
  linear backoff (`Backoff * attempt`).
- **Dead-letter.** When all attempts fail, the message metadata (recipients,
  subject, attempt count, last error) is recorded in
  `notification_dead_letters` and surfaced at
  `GET /v1/workspaces/{wsID}/notifications/dead-letters`. The rendered body is
  **not** stored.
- **Graceful drain.** On shutdown the queue stops accepting and drains buffered
  messages within a bounded window.

### Known durability boundary (deferred)

A message **in flight when the process is killed** (sitting in the in-memory
channel, or mid-backoff during shutdown) is still lost — the queue is in-memory.
Full at-least-once delivery needs a write-ahead outbox table. Dead-letter covers
the common case (SMTP down long enough to exhaust retries); the write-ahead
outbox is filed as a follow-up.

---

## Templates

- One source of truth per event, rendered to **plain-text first, minimal HTML
  second** (`multipart/alternative`).
- **No remote assets, no tracking pixels.** The HTML layout references no
  external images or scripts.
- Every email footer carries an unsubscribe / **manage-preferences** link.

---

## Security posture

- **HTML injection:** all user-supplied content (issue titles, comment bodies)
  is rendered through Go's `html/template`, which auto-escapes. Pinned by
  `render_test.go` (Title and comment-body vectors).
- **SMTP header (CRLF) injection:** header values derived from user content
  (Subject, recipients) are sanitized — CR/LF collapsed to a space — so a
  newline in an issue title cannot inject a `Bcc:` or a fake body. Pinned by
  `smtp_test.go`.
- **Enumeration:** recipients are addressed only by the address the directory
  resolves for their member ID; an unresolved or empty address is dropped. The
  system never sends to a user-supplied per-send address. Pinned by
  `dispatcher_test.go`.
- **Flood / coalescing:** the conservative choice is a **bounded buffer** —
  under a burst the queue stays non-blocking and sheds load rather than growing
  unbounded; recipients are de-duplicated per event. (No cross-event coalescing
  yet — see deferrals.)
- **Credentials:** `EMAIL_SMTP_PASS` is never written to any log line.

---

## Deferrals (filed as issues, intentionally not built)

- Digest / batched-summary emails (coalesce many events into one mail).
- Outbound webhooks as an alternative delivery channel.
- Per-workspace branding (logo / colors / from-name).
- Write-ahead outbox for full at-least-once delivery.
- Default-OFF (opt-in) preference posture for non-mention/assignment events.
- Fail-startup on enabled-but-misconfigured SMTP.
