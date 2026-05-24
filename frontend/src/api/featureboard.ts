import { apiRequest, qs } from "./client";
import type {
  BoardStats,
  FeatureBoard,
  FeaturePost,
  FeaturePostStatus,
  PublicBoardResponse,
} from "./types";

// publicRequest is a fetch wrapper that does NOT auto-attach the
// Authorization header — the public feature-board endpoints must
// stay reachable without credentials. We use bare fetch so a logged-
// in admin's API key never leaks onto the public surface.
async function publicRequest<T>(
  path: string,
  options: { method?: string; body?: unknown; voterEmail?: string } = {},
): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (options.voterEmail) headers["X-Voter-Email"] = options.voterEmail;
  const res = await fetch(path, {
    method: options.method ?? "GET",
    headers,
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      msg = data.error ?? msg;
    } catch {
      // body wasn't JSON
    }
    throw new Error(msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const featureBoardApi = {
  // ─── public ─────────────────────────────────────────────
  getPublicBoard(wsSlug: string, boardSlug: string) {
    return publicRequest<PublicBoardResponse>(
      `/v1/public/boards/${wsSlug}/${boardSlug}`,
    );
  },
  listPublicPosts(
    wsSlug: string,
    boardSlug: string,
    params: { status?: FeaturePostStatus; order_by?: string; voterEmail?: string } = {},
  ) {
    const { voterEmail, ...rest } = params;
    return publicRequest<FeaturePost[]>(
      `/v1/public/boards/${wsSlug}/${boardSlug}/posts${qs(rest as Record<string, string | number | undefined>)}`,
      { voterEmail },
    );
  },
  createPost(
    wsSlug: string,
    boardSlug: string,
    body: { title: string; description: string; author_name: string; author_email: string },
  ) {
    return publicRequest<FeaturePost>(
      `/v1/public/boards/${wsSlug}/${boardSlug}/posts`,
      { method: "POST", body },
    );
  },
  vote(wsSlug: string, boardSlug: string, postID: string, email: string) {
    return publicRequest<{ vote_count: number; voted: boolean }>(
      `/v1/public/boards/${wsSlug}/${boardSlug}/posts/${postID}/vote`,
      { method: "POST", body: { email } },
    );
  },
  unvote(wsSlug: string, boardSlug: string, postID: string, email: string) {
    return publicRequest<{ vote_count: number; voted: boolean }>(
      `/v1/public/boards/${wsSlug}/${boardSlug}/posts/${postID}/vote`,
      { method: "DELETE", body: { email } },
    );
  },

  // ─── admin ──────────────────────────────────────────────
  // These use the standard apiRequest so the workspace API key flows
  // via the Authorization header as on every other admin call.
  listBoards(wsID: string) {
    return apiRequest<FeatureBoard[]>(`/v1/workspaces/${wsID}/boards`);
  },
  createBoard(wsID: string, body: Partial<FeatureBoard>) {
    return apiRequest<FeatureBoard>(`/v1/workspaces/${wsID}/boards`, {
      method: "POST",
      body,
    });
  },
  boardStats(wsID: string, boardID: string) {
    return apiRequest<BoardStats>(`/v1/workspaces/${wsID}/boards/${boardID}/stats`);
  },
  updatePost(
    wsID: string,
    boardID: string,
    postID: string,
    body: { status?: FeaturePostStatus; issue_id?: string | null },
  ) {
    return apiRequest<{ ok: boolean }>(
      `/v1/workspaces/${wsID}/boards/${boardID}/posts/${postID}`,
      { method: "PATCH", body },
    );
  },
  convertPostToIssue(
    wsID: string,
    boardID: string,
    postID: string,
    body: { team_id: string; creator_id: string },
  ) {
    return apiRequest<{ issue_id: string; identifier: string; post_id: string }>(
      `/v1/workspaces/${wsID}/boards/${boardID}/posts/${postID}/convert`,
      { method: "POST", body },
    );
  },
};
