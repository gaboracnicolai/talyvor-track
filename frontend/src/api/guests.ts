import { apiRequest, qs } from "./client";
import type {
  AcceptInviteResponse,
  GuestRecord,
  GuestRole,
  InviteCreateResponse,
  InviteDetail,
  Issue,
} from "./types";

export const guestsApi = {
  // Admin endpoints (member-authenticated).
  list(wsID: string, projectID?: string) {
    return apiRequest<GuestRecord[]>(
      `/v1/workspaces/${wsID}/guests${qs({ project_id: projectID })}`,
    );
  },
  invite(wsID: string, body: { email: string; role: GuestRole; project_id?: string }) {
    return apiRequest<InviteCreateResponse>(`/v1/workspaces/${wsID}/guests/invite`, {
      method: "POST",
      body,
    });
  },
  revoke(wsID: string, id: string) {
    return apiRequest<{ ok: boolean }>(`/v1/workspaces/${wsID}/guests/${id}`, {
      method: "DELETE",
    });
  },
  // Public invite endpoints.
  getInvite(token: string) {
    return apiRequest<InviteDetail>(`/v1/invite/${token}`);
  },
  accept(token: string, name: string) {
    return apiRequest<AcceptInviteResponse>(`/v1/invite/${token}/accept`, {
      method: "POST",
      body: { name },
    });
  },
  // Public guest read endpoints. The Bearer access token must be set
  // in localStorage.track_api_key — apiRequest reads it from there.
  listIssues(wsID: string, projectID: string) {
    return apiRequest<Issue[]>(
      `/v1/guest/workspaces/${wsID}/projects/${projectID}/issues`,
    );
  },
  getIssue(wsID: string, id: string) {
    return apiRequest<Issue>(`/v1/guest/workspaces/${wsID}/issues/${id}`);
  },
};
