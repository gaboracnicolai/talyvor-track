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
TRACK_LENS_API_KEY=tlv_...
TRACK_LENS_WEBHOOK_SECRET=...
```

Every issue now shows how much LLM spend it accrued — both via the 15-minute reconciliation poll and via Lens webhooks for near-real-time updates.

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

Both endpoints return `{"imported": N, "skipped": N, "errors": [...]}` so you can see exactly which rows didn't make it.

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

MIT.
