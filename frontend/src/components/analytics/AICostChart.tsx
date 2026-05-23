import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from "recharts";
import { Sparkles } from "lucide-react";
import type { AICostTrends } from "~/api/types";

interface AICostChartProps {
  trends: AICostTrends;
}

const fmtUSD = (n: number): string => `$${n.toFixed(2)}`;

// The AI cost chart is intentionally the most-prominent analytics
// surface — it's the one chart no other tracker ships. Daily area
// + a projection callout means a CFO can scan it in one sentence.
export function AICostChart({ trends }: AICostChartProps) {
  const data = trends.daily_costs.map((d) => ({
    date: d.date.slice(5, 10),
    Cost: d.cost_usd,
  }));
  return (
    <div className="rounded-md border border-border bg-surface p-4">
      <div className="mb-3 flex items-end justify-between">
        <div>
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            <Sparkles size={14} className="text-accent" />
            AI cost trend
          </h3>
          <div className="mt-1 text-xs text-muted">
            Total {fmtUSD(trends.total_cost_usd)} · avg {fmtUSD(trends.avg_cost_per_issue)} / issue
          </div>
        </div>
        <div className="text-right">
          <div className="font-mono text-xl font-semibold text-accent">
            {fmtUSD(trends.projected_monthly_usd)}
          </div>
          <div className="text-[10px] uppercase tracking-wider text-muted">projected monthly</div>
        </div>
      </div>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <defs>
            <linearGradient id="aiCostFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#f0a030" stopOpacity={0.4} />
              <stop offset="100%" stopColor="#f0a030" stopOpacity={0} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke="#1d2230" strokeDasharray="3 3" />
          <XAxis dataKey="date" stroke="#7a8294" fontSize={10} />
          <YAxis stroke="#7a8294" fontSize={10} />
          <Tooltip
            contentStyle={{
              backgroundColor: "#13161c",
              border: "1px solid #1d2230",
              fontSize: 12,
            }}
          />
          <Area type="monotone" dataKey="Cost" stroke="#f0a030" fill="url(#aiCostFill)" />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
