import { apiRequest, qs } from "./client";
import type { CustomField } from "./types";

export const customFieldsApi = {
  list(wsID: string, teamID?: string) {
    return apiRequest<CustomField[]>(
      `/v1/workspaces/${wsID}/custom-fields${qs({ team_id: teamID })}`,
    );
  },
  create(wsID: string, body: Partial<CustomField>) {
    return apiRequest<CustomField>(`/v1/workspaces/${wsID}/custom-fields`, {
      method: "POST",
      body,
    });
  },
  update(wsID: string, id: string, body: Partial<CustomField>) {
    return apiRequest<CustomField>(`/v1/workspaces/${wsID}/custom-fields/${id}`, {
      method: "PATCH",
      body,
    });
  },
  remove(wsID: string, id: string) {
    return apiRequest<{ ok: boolean }>(`/v1/workspaces/${wsID}/custom-fields/${id}`, {
      method: "DELETE",
    });
  },
  getValues(wsID: string, issueID: string) {
    return apiRequest<Record<string, string>>(
      `/v1/workspaces/${wsID}/issues/${issueID}/fields`,
    );
  },
  setValue(wsID: string, issueID: string, fieldID: string, value: string) {
    return apiRequest<{ ok: boolean }>(
      `/v1/workspaces/${wsID}/issues/${issueID}/fields/${fieldID}`,
      { method: "PUT", body: { value } },
    );
  },
};
