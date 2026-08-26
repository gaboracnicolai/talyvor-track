# The uploaded import file is kept forever

**Measured 2026-08-26 (W3.4, tab-r8kw) on `pgvector/pgvector:pg16` from zero, 26 migrations applied,
through the shipped async runner and the real HTTP handlers. Nothing in this merge changes behaviour.**

## The claim, and what it actually says

`migrations/0020_import_jobs.sql` says of `import_job_payloads`:

> ON DELETE CASCADE ties the blob's lifetime to the job.

That sentence is **true about the constraint** and it is **not a statement about retention**, because
nothing in this product ever ends a job's life. A census of every `DELETE FROM` in non-test Go under
`internal/` and `cmd/` finds **17 production delete statements and not one of them names `import_jobs`
or `import_job_payloads`**. The blob's lifetime is tied to a row nothing deletes.

So the uploaded export — every issue title, description, reporter name and comment the customer
exported out of Jira or Linear, as raw bytes — stays in Postgres indefinitely, long after the import
it existed for has finished.

Measured end to end: a `jira_csv` job driven to `succeeded` through `Runner.RunOnce` leaves its
payload row present and **byte-identical to the upload**, with the issue text readable out of it.

## Why no existing test could see it

`internal/importer/jobs_integration_test.go`'s `TestJob_PayloadAtomicityAndCascade` asserts
*"ON DELETE CASCADE removes the payload with the job"* — by executing `DELETE FROM import_jobs`
**itself, from the test body**. That proves the constraint works. It is silent on whether any code
path ever issues that statement, and none does. A mechanism exercised only by the test that
demonstrates it is inert in the product.

## The one route that could have reached it cannot run

`import_jobs.workspace_id REFERENCES workspaces(id)` carries **no `ON DELETE` clause**, so deleting
the workspace would not cascade to the job either — it would be refused. And the workspace delete is
itself unreachable.

Read from the live catalog (`pg_constraint`), the foreign keys pointing at `workspaces` split:

| action | count | tables |
|---|---|---|
| `NO ACTION` (refuses) | **14** | `ai_spend_events`, `automation_rules`, `cycles`, `import_jobs`, `issue_relations`, `issues`, `labels`, `members`, `milestones`, `notifications`, `projects`, `teams`, `time_entries`, `workspace_integrations` |
| `CASCADE` | 7 | `custom_fields`, `feature_boards`, `feature_posts`, `guest_invites`, `guests`, `issue_scores`, `issue_templates` |

`members` and `teams` are both in the refusing set, and `workspace.Store.CreateWithOwner` — the only
production create path, called by `POST /v1/workspaces` and `POST /v1/bootstrap` — seeds **one member
and one team in the same transaction**. Migration `0025_default_team_backfill.sql` backfilled a team
into every workspace that predates it.

Measured through the real handler, as an **owner** (the one role the route admits):

```
CreateWithOwner  -> members=1 teams=1
DELETE /v1/workspaces/{id} as owner
  -> HTTP 500 DELETE_FAILED
     "update or delete on table \"workspaces\" violates foreign key constraint
      \"members_workspace_id_fkey\" on table \"members\" (SQLSTATE 23503)"
  -> the workspace row survives
```

**`DELETE /v1/workspaces/{wsID}` cannot succeed on any workspace this product creates.** It is
mounted, owner-gated, and returns `{"ok":true}` on a success it can never reach.

## Why *that* was green too — and it is a divergence this repo has already been bitten by once

The route has two tests and both are blind:

1. `internal/workspace/store_test.go#TestDelete_RemovesWorkspace` uses **pgxmock** and hands back
   `NewResult("DELETE", 1)`. A mock cannot see a foreign key.
2. `internal/workspace/owner_gate_test.go#TestWorkspace_Delete_OwnerGated` runs on **real Postgres**
   and asserts a 200 — but seeds with `testutil.DB.Workspace`, which calls `workspace.Store.Create`:
   no owner member, no default team. That workspace deletes cleanly, and it is a shape production
   cannot produce.

The seed helper is the whole suite's default: **351 call sites across 142 test files** use
`d.Workspace(t)`; exactly two real test files use `CreateWithOwner`.

This exact divergence has already cost this repo once. `internal/workspace/first_issue_test.go`
records the create path being *"unreachable for every new user, on every deployment"* because a
bootstrapped workspace held zero teams — found only when a test finally seeded through
`CreateWithOwner`, and fixed by migration 0025. **This is the same seam, on the delete path.**

## Why nothing was changed

Two candidate repairs, both product decisions:

1. **Cascade.** Add `ON DELETE CASCADE` to the 14 refusing foreign keys, matching the convention the
   7 newer tables already use. One owner-authenticated `DELETE` then destroys every issue, comment,
   time entry, spend event and uploaded payload in the tenant, irreversibly and with no confirmation
   step. That is a data-destruction policy, not a default.
2. **Refuse honestly.** Detect a non-empty workspace and answer a clear 4xx naming what blocks it,
   instead of a 500 carrying a raw Postgres constraint string to the caller. This makes the current
   behaviour truthful but decides that workspaces are not deletable, which contradicts a shipped
   route.

Either way the retention question is separate and still open: **how long should an uploaded import
payload be kept?** The import needs it only until the job reaches a terminal state.

## `0020`'s sentence cannot be corrected in place

`internal/migrate` records a **sha256 per migration file** in `schema_migrations` and refuses to run
on a mismatch (*"checksum mismatch / missing file / gap — refuse before touching anything"*). Editing
even a comment in an applied migration stops every deployed database from migrating. So the
correction lives in this document and in `internal/importer/payload_lifetime_test.go`, not in 0020.

## What is pinned, and what proved the pins can fail

`internal/importer/payload_lifetime_test.go` holds four assertions: the payload outlives a finished
import; no production path deletes a job or its payload (a census carrying its own positive control);
the workspace cascade cannot run (with the Create-only fixture shape asserted alongside as the
must-stay-green companion); and the catalog's refuse/cascade split, pinned by table name.

**All four passed on their first run**, so each is controlled in
`scripts/w34-payload-lifetime-controls-r8kw.py` — **8/8 as predicted**, one mutation at a time,
restored in a `finally` and sha256-verified:

| control | mutation | predicted catcher |
|---|---|---|
| C1 | `JobStore.Finish` deletes the payload (literal SQL) | payload test **and** census |
| C2 | a production `DELETE FROM import_jobs` nothing calls | census **only** — behaviour unchanged |
| C3 | the same deletion, statement built at runtime | payload test **only** — the census's measured blindness |
| C4 | the census's matcher blinded | census **REFUSES**, never reports a clean product |
| C5 | `members` + `teams` cascade | catalog census only (see below) |
| C5b | `members` + `teams` + `import_jobs` all cascade | workspace test **and** catalog census |
| C6 | `testutil.Workspace` seeds like production | workspace test — arm B's premise fails |
| C7 | `columnIndex.get` names the last occurrence | **nothing** (must stay green); the package's own tests go red |

Two of these started as mispredictions and are recorded rather than tuned away:

- **C5** was predicted to turn the workspace delete green and did not. Arm A's workspace also holds an
  import job, and `import_jobs` refuses independently — so a partial fix does not move that
  assertion. C5b was added afterwards to prove arm A can move at all.
- **C7**'s first reality check ran `go test -run TestCSV|TestJiraCSV|TestLinearCSV` and reported
  GREEN. The filter was not the problem the exit code suggested: **the whole `internal/importer`
  package is green** for the mutation I first chose (`columnIndex.get` no longer trimming
  whitespace) — a real behaviour change in the CSV reader that **129 matched tests and the full
  package both fail to catch**. Noted and not ridden on this diff. The control was re-cut onto the
  last-occurrence mutation, which `TestColumnIndex_GetNamesTheFirstOccurrenceNotTheLast` does catch.
