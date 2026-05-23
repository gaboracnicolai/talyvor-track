import { Sparkles } from "lucide-react";
import { Tooltip } from "~/components/ui/Tooltip";
import clsx from "clsx";

interface AICostBadgeProps {
  costUSD: number;
  tokens?: number;
  className?: string;
}

const fmt = (n: number): string => {
  if (n >= 1) return `$${n.toFixed(2)}`;
  if (n >= 0.01) return `$${n.toFixed(2)}`;
  return `$${n.toFixed(3)}`;
};

// Talyvor-only affordance. No other tracker surfaces per-issue LLM
// spend at the row level — it's the visible payoff of the Lens
// integration and the reason Track gets installed in the first
// place. Don't conflate this with a generic "cost" pill.
export function AICostBadge({ costUSD, tokens, className }: AICostBadgeProps) {
  if (!costUSD || costUSD <= 0) return null;
  const tooltip = tokens
    ? `${fmt(costUSD)} · ${tokens.toLocaleString()} tokens via Lens`
    : `${fmt(costUSD)} via Lens`;
  return (
    <Tooltip content={tooltip}>
      <span
        className={clsx(
          "inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent",
          className,
        )}
      >
        <Sparkles size={10} />
        {fmt(costUSD)}
      </span>
    </Tooltip>
  );
}
