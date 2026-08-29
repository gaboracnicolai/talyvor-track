import { apiRequest, qs } from "./client";
import type {
  TimeEntryRecord,
  TimeSummary,
  TimerState,
  WorkspaceTimeSummary,
} from "./types";

// ⚠ member_id IS NO LONGER SENT ON ANY TIMER CALL. SEC-5 retired the caller-supplied member
// id from all four timer paths — the server takes the actor from the gateway-verified session
// (authz.MemberID) and never from the request — and it retired it by DELETING the field from the
// decode structs. httpx.DecodeJSON calls DisallowUnknownFields, so a body that still carried
// `member_id` was a 400 BAD_JSON before any handler logic ran: START TIMER AND LOG TIME WERE BOTH
// DEAD IN THE SHIPPED UI, with a correct path, a correct method and a correct response type, which
// is why the route census and the response-field census both stayed green over it.
// api/websocket.ts made exactly this correction for /v1/ws in #44 ("member_id is no longer sent");
// these four call sites were missed. internal/timetracking/spa_body_contract_realpg_test.go drives
// the two write paths with the body this file sends and is what keeps them wired.
export const timeApi = {
  getTimer(wsID: string) {
    return apiRequest<TimerState>(`/v1/workspaces/${wsID}/timer`);
  },
  startTimer(wsID: string, issueID: string, description = "") {
    return apiRequest<TimeEntryRecord>(`/v1/workspaces/${wsID}/timer/start`, {
      method: "POST",
      body: { issue_id: issueID, description },
    });
  },
  stopTimer(wsID: string) {
    return apiRequest<TimeEntryRecord | { ok: false }>(
      `/v1/workspaces/${wsID}/timer/stop`,
      { method: "POST" },
    );
  },
  logTime(
    wsID: string,
    body: {
      issue_id: string;
      description: string;
      started_at: string;
      stopped_at: string;
      billable?: boolean;
    },
  ) {
    return apiRequest<TimeEntryRecord>(`/v1/workspaces/${wsID}/time-entries`, {
      method: "POST",
      body,
    });
  },
  remove(wsID: string, id: string) {
    return apiRequest<{ ok: boolean }>(
      `/v1/workspaces/${wsID}/time-entries/${id}`,
      { method: "DELETE" },
    );
  },
  listForIssue(wsID: string, issueID: string) {
    return apiRequest<{ entries: TimeEntryRecord[]; summary: TimeSummary }>(
      `/v1/workspaces/${wsID}/issues/${issueID}/time-entries`,
    );
  },
  workspaceSummary(wsID: string, since: string) {
    return apiRequest<WorkspaceTimeSummary>(
      `/v1/workspaces/${wsID}/time-summary${qs({ since })}`,
    );
  },
};
