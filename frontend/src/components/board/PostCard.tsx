import type { FeaturePost } from "~/api/types";
import { BoardStatusBadge } from "./StatusBadge";
import { VoteButton } from "./VoteButton";

interface PostCardProps {
  post: FeaturePost;
  pending?: boolean;
  onVote: () => void;
}

// One row on the public board: vote button on the left, post body
// on the right with status + author + linked-issue hint.
export function PostCard({ post, pending, onVote }: PostCardProps) {
  return (
    <article className="flex gap-3 rounded-md border border-border bg-surface p-3">
      <VoteButton
        count={post.vote_count}
        voted={!!post.has_voted}
        pending={pending}
        onClick={onVote}
      />
      <div className="min-w-0 flex-1">
        <div className="mb-1 flex flex-wrap items-center gap-2">
          <h3 className="truncate text-sm font-semibold">{post.title}</h3>
          <BoardStatusBadge status={post.status} />
          {post.issue_id ? (
            <span className="text-[10px] text-muted">
              linked → <span className="font-mono">{post.issue_id.slice(0, 8)}</span>
            </span>
          ) : null}
        </div>
        {post.description ? (
          <p className="line-clamp-3 whitespace-pre-wrap text-xs text-muted">
            {post.description}
          </p>
        ) : null}
        <div className="mt-1 text-[10px] text-muted">
          {post.author_name || "Anonymous"} · {new Date(post.created_at).toLocaleDateString()}
        </div>
      </div>
    </article>
  );
}
