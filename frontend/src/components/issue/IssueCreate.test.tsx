import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { IssueCreate } from "./IssueCreate";

// The create dialog against a fake Track API: the real hook, the real
// client, and only fetch replaced, so a POST here is the request the
// browser would send.
function fakeTrack({ teams }: { teams: Promise<unknown[]> }) {
  const posts: Record<string, unknown>[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const json = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (init?.method === "POST" && url.endsWith("/issues")) {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        posts.push(body);
        return json({ id: "iss-1", identifier: "TRK-1", title: body.title }, 201);
      }
      if (url.endsWith("/teams")) return json(await teams);
      return json([]);
    }),
  );
  return posts;
}

function renderDialog() {
  const onClose = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <IssueCreate open onClose={onClose} />
    </QueryClientProvider>,
  );
  return { onClose, title: screen.getByPlaceholderText("Issue title") as HTMLInputElement };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("IssueCreate", () => {
  it("creates the issue on Enter and empties the title", async () => {
    const posts = fakeTrack({ teams: Promise.resolve([{ id: "team-1", identifier: "TRK", name: "Track" }]) });
    const { onClose, title } = renderDialog();
    await waitFor(() => expect(screen.getByRole("option", { name: /TRK/ })).toBeTruthy());

    fireEvent.change(title, { target: { value: "Ship the wallet statement" } });
    fireEvent.keyDown(title, { key: "Enter" });

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ title: "Ship the wallet statement", team_id: "team-1" });
    expect(title.value).toBe("");
  });

  it("creates the issue from the button before the teams list has loaded", async () => {
    const posts = fakeTrack({ teams: new Promise(() => {}) });
    const { onClose, title } = renderDialog();

    fireEvent.change(title, { target: { value: "Pay a supplier from an agent" } });
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(posts).toHaveLength(1);
    // No team_id: the server files it into the workspace's sole team.
    expect(posts[0]).toMatchObject({ title: "Pay a supplier from an agent" });
    expect(posts[0]).not.toHaveProperty("team_id");
    expect(title.value).toBe("");
  });
});
