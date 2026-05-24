import { apiRequest, qs } from "./client";
import type {
  TimeEntryRecord,
  TimeSummary,
  TimerState,
  WorkspaceTimeSummary,
} from "./types";

export const timeApi = {
  getTimer(wsID: string, memberID: string) {
    return apiRequest<TimerState>(
      `/v1/workspaces/${wsID}/timer${qs({ member_id: memberID })}`,
    );
  },
  startTimer(wsID: string, memberID: string, issueID: string, description = "") {
    return apiRequest<TimeEntryRecord>(`/v1/workspaces/${wsID}/timer/start`, {
      method: "POST",
      body: { member_id: memberID, issue_id: issueID, description },
    });
  },
  stopTimer(wsID: string, memberID: string) {
    return apiRequest<TimeEntryRecord | { ok: false }>(
      `/v1/workspaces/${wsID}/timer/stop`,
      { method: "POST", body: { member_id: memberID } },
    );
  },
  logTime(
    wsID: string,
    body: {
      issue_id: string;
      member_id: string;
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
