import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, MessageSquare, Copy, ArrowRightCircle } from "lucide-react";
import { featureBoardApi } from "~/api/featureboard";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";
import { useWorkspace } from "~/hooks/useWorkspace";
import { useUIStore } from "~/stores/ui";
import type { FeatureBoard, FeaturePostStatus } from "~/api/types";

// Admin surface for feature boards. Two stacked sections — board
// list (with "create board") + posts for the selected board (with
// per-post status management + convert-to-issue).
export function FeatureBoardsPage() {
  const { workspaceId } = useWorkspace();
  const boards = useQuery({
    queryKey: ["feature-boards", workspaceId],
    queryFn: () => featureBoardApi.listBoards(workspaceId),
    enabled: !!workspaceId,
  });
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const selectedBoard =
    boards.data?.find((b) => b.id === selectedID) ?? boards.data?.[0] ?? null;

  return (
    <div className="flex h-full">
      <aside className="flex w-72 shrink-0 flex-col border-r border-border bg-surface">
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <MessageSquare size={14} className="text-accent" />
            Feature boards
          </div>
        </div>
        <CreateBoardForm />
        <div className="flex-1 overflow-y-auto p-1">
          {boards.isLoading ? (
            <div className="px-2 py-3 text-xs text-muted">Loading…</div>
          ) : (boards.data ?? []).length === 0 ? (
            <div className="px-2 py-3 text-xs text-muted">
              No boards yet. Create one to start collecting feedback.
            </div>
          ) : (
            boards.data!.map((b) => (
              <BoardListItem
                key={b.id}
                board={b}
                active={selectedBoard?.id === b.id}
                onClick={() => setSelectedID(b.id)}
              />
            ))
          )}
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto p-4">
        {selectedBoard ? <BoardPosts board={selectedBoard} /> : (
          <div className="text-sm text-muted">Select a board to manage posts.</div>
        )}
      </main>
    </div>
  );
}

function CreateBoardForm() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () =>
      featureBoardApi.createBoard(workspaceId, {
        name: name.trim(),
        public: true,
        allow_anonymous: true,
      }),
    onSuccess: () => {
      setName("");
      qc.invalidateQueries({ queryKey: ["feature-boards"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
  return (
    <div className="flex items-center gap-1 border-b border-border px-2 py-2">
      <Input
        placeholder="New board name"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <Button size="sm" onClick={() => create.mutate()} disabled={create.isPending || !name.trim()}>
        <Plus size={12} />
      </Button>
    </div>
  );
}

function BoardListItem({
  board,
  active,
  onClick,
}: {
  board: FeatureBoard;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={
        "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm " +
        (active ? "bg-bg text-text" : "text-muted hover:bg-bg/60 hover:text-text")
      }
    >
      <span className="truncate">{board.name}</span>
      {board.public ? (
        <span className="ml-auto text-[10px] uppercase tracking-wider text-muted">
          public
        </span>
      ) : null}
    </button>
  );
}

function BoardPosts({ board }: { board: FeatureBoard }) {
  const { workspaceId } = useWorkspace();
  const toast = useUIStore((s) => s.toast);
  const qc = useQueryClient();
  // Until a dedicated admin posts list endpoint lands, admins
  // manage status by pasting a post ID from the public board.
  // Simple and avoids re-implementing the public list query under
  // a separate auth surface.
  const [postID, setPostID] = useState("");
  const [status, setStatus] = useState<FeaturePostStatus>("planned");

  const update = useMutation({
    mutationFn: () =>
      featureBoardApi.updatePost(workspaceId, board.id, postID.trim(), { status }),
    onSuccess: () => {
      toast("Post updated", "success");
      qc.invalidateQueries({ queryKey: ["public-board-posts"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });

  const inviteURL = `${window.location.origin}/board/${getWorkspaceSlug()}/${board.slug}`;

  return (
    <div className="space-y-4">
      <header className="space-y-1">
        <h2 className="text-lg font-semibold">{board.name}</h2>
        {board.description ? (
          <p className="text-sm text-muted">{board.description}</p>
        ) : null}
        <div className="flex items-center gap-2 rounded-md border border-border bg-bg px-2 py-1.5 text-xs">
          <span className="text-muted">Public URL:</span>
          <code className="flex-1 truncate font-mono text-[10px]">{inviteURL}</code>
          <button
            onClick={() => {
              void navigator.clipboard.writeText(inviteURL);
              toast("Copied", "success");
            }}
            className="text-muted hover:text-text"
            title="Copy"
          >
            <Copy size={12} />
          </button>
        </div>
      </header>

      <section className="space-y-2 rounded-md border border-border bg-surface p-3">
        <h3 className="text-sm font-semibold">Manage post status</h3>
        <p className="text-xs text-muted">
          Paste a post ID from the public board to change its status. Convert
          to a Track issue from the public page to wire it into the planning
          surface.
        </p>
        <div className="flex items-center gap-2">
          <Input
            placeholder="post id"
            value={postID}
            onChange={(e) => setPostID(e.target.value)}
          />
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value as FeaturePostStatus)}
            className="h-9 rounded-md border border-border bg-bg px-2 text-sm"
          >
            <option value="open">Open</option>
            <option value="planned">Planned</option>
            <option value="in_progress">In progress</option>
            <option value="completed">Completed</option>
            <option value="declined">Declined</option>
          </select>
          <Button onClick={() => update.mutate()} disabled={!postID.trim() || update.isPending}>
            <ArrowRightCircle size={12} /> Apply
          </Button>
        </div>
      </section>

    </div>
  );
}

// getWorkspaceSlug is a placeholder — the workspace store currently
// holds workspace_id, not slug. Until the admin shell knows the
// slug, we surface a copy-able URL with the workspace_id; users can
// replace it manually.
function getWorkspaceSlug(): string {
  return localStorage.getItem("track_workspace_slug") ?? "default";
}
