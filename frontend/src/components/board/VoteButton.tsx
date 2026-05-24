import clsx from "clsx";
import { ChevronUp } from "lucide-react";

interface VoteButtonProps {
  count: number;
  voted: boolean;
  pending?: boolean;
  onClick: () => void;
}

// Vertical pill — count above the chevron — that toggles on click.
// Designed to read at a glance in a tightly-packed list.
export function VoteButton({ count, voted, pending, onClick }: VoteButtonProps) {
  return (
    <button
      onClick={onClick}
      disabled={pending}
      className={clsx(
        "flex h-14 w-12 shrink-0 flex-col items-center justify-center rounded-md border transition-colors",
        voted
          ? "border-accent bg-accent/10 text-accent"
          : "border-border text-muted hover:border-accent/40 hover:text-text",
        pending ? "opacity-60" : "",
      )}
      aria-pressed={voted}
      title={voted ? "Click to remove your vote" : "Click to upvote"}
    >
      <span className="font-mono text-sm font-semibold">{count}</span>
      <ChevronUp size={14} />
    </button>
  );
}
