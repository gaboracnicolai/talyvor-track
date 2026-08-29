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
  // ⚠ NARROWER THAN Partial<CustomField> ON PURPOSE, AND THE TYPE IS THE ONLY PLACE THAT SAYS SO.
  // PATCH decodes into an anonymous struct of exactly {name, options, required} — the store's
  // UpdateField takes those three and nothing else, because changing a live field's `type` is a
  // question about the values already stored under it, not an edit. httpx.DecodeJSON uses
  // DisallowUnknownFields, so `Partial<CustomField>` was a signature offering six more keys
  // (created_at, id, position, team_id, type, workspace_id) that each turn the request into a
  // 400 BAD_JSON. No caller passes them today — this function has no consumer in the SPA at all —
  // so it was latent rather than shipped, which is exactly the state the timer paths were in
  // before someone wired a UI to them (W3.68, `8359a30`).
  update(
    wsID: string,
    id: string,
    body: Pick<CustomField, "name" | "options" | "required">,
  ) {
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
