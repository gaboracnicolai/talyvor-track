# Talyvor Track

**AI-native issue tracker — the only issue tracker that shows you what your AI development actually costs.**

Track is a fast, keyboard-first issue tracker built around one idea: if your team is using LLMs (Claude, Codex, custom agents) to write code, you should be able to see how much that costs *per issue* — not just on a billing dashboard, not just at the org level. Tracker rows show LLM spend the way Jira shows story points.

It integrates natively with [Talyvor Lens](https://github.com/gaboracnicolai/talyvor-lens) for cost attribution and exposes an MCP server so AI agents can create, update, and triage issues without leaving the terminal.

## Why Talyvor Track?

| Feature                   | Jira | Linear | **Talyvor Track** |
| ------------------------- | ---- | ------ | ----------------- |
| Fast & keyboard-first     | ❌   | ✅     | ✅                |
| Non-engineering teams     | ⚠️   | ❌     | ✅                |
| **AI cost per issue**     | ❌   | ❌     | ✅                |
| Built-in automation       | ⚠️   | ❌     | ✅                |
| Semantic search           | ❌   | ❌     | ✅                |
| AI sprint planning        | ❌   | ❌     | ✅                |
| MCP integration           | ❌   | ✅     | ✅                |
| Self-hosted               | ❌   | ❌     | ✅                |
| Real-time updates         | ✅   | ✅     | ✅                |

## Quick start (2 commands)

```bash
cp .env.example .env  # add your API keys
docker compose up -d
```

- Web UI: http://localhost:5173
- API: http://localhost:3000
- Health: http://localhost:3000/healthz

The stack runs `track`, `frontend` (nginx-served SPA), `postgres` (pgvector for semantic search), and `redis`.

## Connect to Talyvor Lens

Set your Lens URL for AI cost attribution:

```bash
TRACK_LENS_URL=http://your-lens:8080
TRACK_LENS_API_KEY=tlv_...         # a Lens WORKSPACE key — reads per-issue spend
TRACK_LENS_MINT_KEY=               # the value of Lens's LENS_MINT_KEY — required for AI features
TRACK_LENS_WEBHOOK_SECRET=...
TRACK_LENS_DASHBOARD_URL=          # optional; where `lens_url` points. Unset ⇒ no link.
```

Every issue now shows how much LLM spend it accrued — both via the 15-minute reconciliation poll and via Lens webhooks for near-real-time updates.

### Two Lens credentials, and why one cannot do both jobs

These are different keys on purpose, and setting only the first is why Track's AI features do
nothing:

| variable | what it is | what it is for |
|---|---|---|
| `TRACK_LENS_API_KEY` | a Lens **workspace** key (`tlv_…`) | reading per-issue spend from `/v1/api/spend/*` |
| `TRACK_LENS_MINT_KEY` | the value of Lens's **`LENS_MINT_KEY`** | minting the per-workspace token every AI feature needs |

Lens resolves a workspace from a `tlv_` key, which is what the spend reads require — and a `tlv_`
key authenticates with `IsAdmin=false`, so it is refused by the token-mint endpoint. The mint
credential is the reverse: it carries no workspace, so it can mint and cannot read spend. No single
value satisfies both.

> **⚠ Never set either of these to Lens's `LENS_API_KEY`.** That global admin key would satisfy
> both — which is exactly the problem. It grants LXC grants, royalty adjudication and minting for
> every tenant, so a Track compromise would become Lens admin over the whole deployment.
> `LENS_MINT_KEY` exists precisely so Track does not have to hold it: its only power is minting a
> per-workspace token. It authenticates with `IsAdmin=false`, so it fails every one of Lens's
> admin gates by construction rather than by a list someone has to maintain.

With `TRACK_LENS_MINT_KEY` unset, Track keeps working and every AI feature says so plainly rather
than failing — the API returns `{"ai_available": false, "reason": "…"}` naming the variable to set.

## MCP integration (Claude Code, Codex, custom agents)

Add to your MCP config:

```json
{
  "mcpServers": {
    "talyvor-track": {
      "url": "http://localhost:3000/mcp"
    }
  }
}
```

Twelve tools are exposed: `create_issue`, `update_issue`, `get_issue`, `list_issues`, `search_issues`, `add_comment`, `get_sprint_status`, `triage_issue`, `get_ai_costs`, `move_to_cycle`, `create_project`, `list_team_members`.

`get_ai_costs` is unique to Track — no other issue tracker exposes per-workspace LLM cost data through MCP.

## Migrate from Linear

```bash
# 1. Export from Linear: Settings → Export data → CSV
# 2. Import to Track:
curl -X POST "http://localhost:3000/v1/import/linear?workspace_id=WS&team_id=TEAM" \
  -F "file=@linear-export.csv"
```

Status mapping: `Backlog → backlog`, `Todo → todo`, `In Progress → in_progress`, `Done → done`, `Cancelled → cancelled`.

## Migrate from Jira

```bash
# 1. Export from Jira: Issues → Export → CSV (Current fields)
# 2. Import to Track:
curl -X POST "http://localhost:3000/v1/import/jira?workspace_id=WS&team_id=TEAM" \
  -F "file=@jira-export.csv"
```

Priority mapping: `Highest → urgent`, `High/Major → high`, `Medium → medium`, `Low → low`, `Lowest/Trivial → low`. Status mapping collapses `Done`, `Closed`, and `Resolved` onto Track's `done`.

⚠ **A `Closed` issue is not necessarily finished work, and the CSV says which.** On a real Jira the
status of abandoned work is also `Closed` — what distinguishes it is the `Resolution` column. So the
CSV import reads it, and it can do exactly one thing: move a row that mapped to `done` to
`cancelled`, when the resolution is a word Track's own status vocabulary already reads that way
(`Won't Fix`, `Won't Do`). Such a row records no completion time, because Track records one only on
`done` — and analytics' cycle-time and throughput select on `completed_at IS NOT NULL` with no
status predicate, so an abandoned issue carrying one is counted as delivered.

⚠ **Export the `Created` column, or every cycle-time number you get back is wrong.** Track stores an
issue's creation time in a column the database defaults to "now", so an import that cannot read
`Created` records every issue as opened at import time — and time to resolution is computed as
`completed_at − created_at`, which then comes out NEGATIVE. Measured on a real export: issues a
median of 332 days old, a true median cycle time of 687 hours, and Track computing −6,543. The CSV
import reads the column and, if it is missing or in a date format Track does not recognise, **says
so in `warnings`** rather than quietly recording today's date. (`Export → CSV (All fields)` always
carries it; a narrowed export may not.)

The **API imports read the same thing** — Jira's `created` and Linear's `createdAt` — and report the
same way. They needed a separate fix from the CSV one because they write through a different
statement, and until they got it they had the identical defect: measured against 100 real resolved
issues on a live Jira Cloud, a true median cycle time of 88.7 hours against Track computing −408.3,
with **100 of 100 negative**. If a provider response arrives without the field, the job says so in
`warnings`; the row still imports, and its opening time is the import instant.

Every other resolution **changes nothing and is reported in `warnings`** with the number of issues
that carried it — `Duplicate`, `Timed out`, `Obsolete` and the rest plainly describe abandoned work
too, and deciding which of them Track should treat as cancelled is a product judgement, not
something the importer should guess. The first import tells you which ones your instance uses.

Both endpoints return `{"imported": N, "skipped": N, "refused": N, "errors": [...], "warnings": [...]}` so you
can see exactly which rows didn't make it, and why they didn't:

- **skipped** — rows that FAILED: a malformed row, a transport error, a rejected write.
- **refused** — rows the importer DECLINED to write, by policy, with nothing wrong. An API import
  carries the provider's own key (`ENG-123`), and Track derives native identifiers in that same
  shape, so the two can collide. When they do, the importer will not overwrite an issue a person
  created — it reports the row instead. A refusal is the protection working, not an error.
- **warnings** — rows that DID import, with a field the mapper could not place on Track's scale
  (an unknown status, a date in an unrecognised format). Bounded to ten exemplars per kind, with
  the totals always reported.

The async job endpoints (`/v1/import/jobs/{id}`) report the same three counts as `imported` /
`failed` / `skipped` columns, where `failed` is the FAILED count and `skipped` is the REFUSED count.
⚠ The two spellings do cross over, which is a known wart: `ImportResult.skipped` (inline) means
failures, while `import_jobs.skipped` (async) means refusals. Renaming the inline field would break
a shipped JSON contract, so it is documented here rather than changed quietly.

## Development

```bash
make dev              # backend + Vite dev server side-by-side
make test             # go test -race -count=1 ./...
make frontend-build   # production frontend build
```

CI runs three jobs: Go test (race detector enabled), frontend typecheck + build, multi-arch container publish to `ghcr.io/<owner>/talyvor-track`. PRs validate test + frontend; only main triggers publish.

## Architecture

| Layer       | Tech                                                              |
| ----------- | ----------------------------------------------------------------- |
| API         | Go 1.25, chi router, pgx, no ORM                                  |
| Realtime    | gorilla/websocket, single-process hub                             |
| Database    | Postgres 16 + pgvector (for semantic search)                      |
| Cache       | Redis 7                                                           |
| Frontend    | React 18, Vite, TanStack Query, Zustand, Radix UI, Tailwind       |
| AI          | Routed through Talyvor Lens — never talks to providers directly   |
| MCP         | JSON-RPC 2.0 over HTTP + SSE, protocol version `2024-11-05`       |

## License

[Business Source License 1.1](LICENSE) (BUSL-1.1). **Not an open-source licence today.**

You may read, modify and self-host Talyvor Track, including in production, for your own
organisation's purposes without limit, and an integrator may run it for up to **three clients
at a time**, each on its own deployment. You may **not** run one deployment serving two or more
unrelated organisations. Beyond three concurrent client engagements, or for multi-tenant use,
that is a commercial licence rather than a refusal — `hello@talyvor.com`. See the `Additional Use Grant` in [LICENSE](LICENSE)
for the exact boundary, and the `Change Date`, on which this converts to Apache License 2.0.
