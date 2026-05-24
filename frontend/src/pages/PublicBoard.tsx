import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import clsx from "clsx";
import { featureBoardApi } from "~/api/featureboard";
import { PostCard } from "~/components/board/PostCard";
import { SubmitForm } from "~/components/board/SubmitForm";
import { FooterBranding } from "./InviteAccept";
import type { FeaturePost, FeaturePostStatus } from "~/api/types";

interface PublicBoardProps {
  wsSlug: string;
  boardSlug: string;
}

// Filter buckets in display order. All-status is the default — most
// public roadmaps want the busiest view on top.
const filters: { value: FeaturePostStatus | "all"; label: string }[] = [
  { value: "all", label: "All" },
  { value: "open", label: "Open" },
  { value: "planned", label: "Planned" },
  { value: "in_progress", label: "In progress" },
  { value: "completed", label: "Completed" },
];

const emailKey = "track_board_voter_email";
const nameKey = "track_board_voter_name";

export function PublicBoardPage({ wsSlug, boardSlug }: PublicBoardProps) {
  const [filter, setFilter] = useState<FeaturePostStatus | "all">("all");
  // Persist the voter's email so they can vote / post without
  // retyping. localStorage scope is intentional — guests already
  // share an email-based identity model elsewhere in Track.
  const [email, setEmail] = useState(
    () => (typeof window !== "undefined" && localStorage.getItem(emailKey)) || "",
  );
  const name =
    (typeof window !== "undefined" && localStorage.getItem(nameKey)) || "";

  const board = useQuery({
    queryKey: ["public-board", wsSlug, boardSlug],
    queryFn: () => featureBoardApi.getPublicBoard(wsSlug, boardSlug),
  });

  const posts = useQuery({
    queryKey: ["public-board-posts", wsSlug, boardSlug, filter, email],
    queryFn: () =>
      featureBoardApi.listPublicPosts(wsSlug, boardSlug, {
        status: filter === "all" ? undefined : (filter as FeaturePostStatus),
        order_by: "votes",
        voterEmail: email || undefined,
      }),
  });

  const qc = useQueryClient();
  const vote = useMutation({
    mutationFn: (post: FeaturePost) =>
      post.has_voted
        ? featureBoardApi.unvote(wsSlug, boardSlug, post.id, email)
        : featureBoardApi.vote(wsSlug, boardSlug, post.id, email),
    onMutate: async (post) => {
      // Optimistic toggle: apply the vote delta on the current list
      // before the network round-trip lands.
      await qc.cancelQueries({ queryKey: ["public-board-posts", wsSlug, boardSlug] });
      const snapshots: Array<[readonly unknown[], FeaturePost[] | undefined]> = [];
      qc.getQueriesData<FeaturePost[]>({
        queryKey: ["public-board-posts", wsSlug, boardSlug],
      }).forEach(([key, value]) => {
        snapshots.push([key, value]);
        if (!value) return;
        qc.setQueryData(
          key,
          value.map((p) =>
            p.id === post.id
              ? {
                  ...p,
                  has_voted: !p.has_voted,
                  vote_count: p.vote_count + (p.has_voted ? -1 : 1),
                }
              : p,
          ),
        );
      });
      return { snapshots };
    },
    onError: (_err, _vars, ctx) => {
      ctx?.snapshots.forEach(([key, value]) => qc.setQueryData(key, value));
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["public-board-posts", wsSlug, boardSlug] });
    },
  });

  const create = useMutation({
    mutationFn: (body: {
      title: string;
      description: string;
      author_name: string;
      author_email: string;
    }) => featureBoardApi.createPost(wsSlug, boardSlug, body),
    onSuccess: (_post, body) => {
      // Stash the user's email + name so subsequent posts skip the
      // re-typing step.
      localStorage.setItem(emailKey, body.author_email);
      if (body.author_name) localStorage.setItem(nameKey, body.author_name);
      setEmail(body.author_email);
      qc.invalidateQueries({ queryKey: ["public-board-posts", wsSlug, boardSlug] });
    },
  });

  const onVote = (post: FeaturePost) => {
    if (!email) {
      const supplied = window.prompt("Your email (used only to dedupe votes):");
      if (!supplied) return;
      localStorage.setItem(emailKey, supplied);
      setEmail(supplied);
      // Vote with the fresh email — schedule on the next tick so
      // the state update lands before the mutation fires.
      setTimeout(() => vote.mutate({ ...post, has_voted: false }), 0);
      return;
    }
    vote.mutate(post);
  };

  const list = useMemo(() => posts.data ?? [], [posts.data]);

  // Edge case: the public-board route is loaded outside the admin
  // shell, so a 404 must render directly here.
  if (board.error) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-bg text-text">
        <div className="text-sm text-priority-urgent">
          {(board.error as Error).message}
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen flex-col bg-bg text-text">
      <header className="border-b border-border bg-surface px-6 py-4">
        <div className="flex items-center gap-2">
          <div className="flex h-7 w-7 items-center justify-center rounded bg-accent text-bg">
            <span className="font-mono text-sm font-bold">T</span>
          </div>
          <div className="flex-1">
            <h1 className="text-lg font-semibold">
              {board.data?.board.name ?? "Loading…"}
            </h1>
            {board.data?.board.description ? (
              <p className="text-xs text-muted">{board.data.board.description}</p>
            ) : null}
          </div>
          {board.data?.stats ? (
            <div className="text-right text-[10px] text-muted">
              {board.data.stats.total_posts} ideas · {board.data.stats.total_votes} votes
            </div>
          ) : null}
        </div>
      </header>

      <main className="flex flex-1 flex-col gap-4 px-6 py-4 lg:flex-row">
        <aside className="shrink-0 lg:w-40">
          <h2 className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
            Filter
          </h2>
          <div className="flex flex-row gap-1 lg:flex-col">
            {filters.map((f) => (
              <button
                key={f.value}
                onClick={() => setFilter(f.value)}
                className={clsx(
                  "h-7 rounded px-2 text-xs",
                  filter === f.value
                    ? "bg-surface text-text"
                    : "text-muted hover:bg-surface/60 hover:text-text",
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
        </aside>

        <section className="flex-1 space-y-2">
          {posts.isLoading ? (
            <p className="text-sm text-muted">Loading posts…</p>
          ) : list.length === 0 ? (
            <p className="text-sm text-muted">No posts yet — be the first to share an idea.</p>
          ) : (
            list.map((p) => (
              <PostCard
                key={p.id}
                post={p}
                pending={vote.isPending}
                onVote={() => onVote(p)}
              />
            ))
          )}
        </section>

        <aside className="lg:w-72">
          <SubmitForm
            defaultEmail={email}
            defaultName={name}
            pending={create.isPending}
            onSubmit={(body) => create.mutate(body)}
          />
          {create.error ? (
            <p className="mt-2 text-xs text-priority-urgent">
              {(create.error as Error).message}
            </p>
          ) : null}
        </aside>
      </main>

      <footer className="border-t border-border bg-surface px-6 py-3">
        <FooterBranding />
      </footer>
    </div>
  );
}

// usePublicBoardRoute parses /board/<wsSlug>/<boardSlug>. Returns
// null when the URL doesn't match; the App component branches on it
// the same way it does for the invite-accept page.
export function usePublicBoardRoute(): { wsSlug: string; boardSlug: string } | null {
  const [route, setRoute] = useState(() => parseBoardURL());
  useEffect(() => {
    const onPop = () => setRoute(parseBoardURL());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  return route;
}

function parseBoardURL(): { wsSlug: string; boardSlug: string } | null {
  if (typeof window === "undefined") return null;
  const m = window.location.pathname.match(/^\/board\/([^/]+)\/([^/]+)/);
  return m ? { wsSlug: m[1], boardSlug: m[2] } : null;
}
