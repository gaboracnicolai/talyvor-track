import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  Legend,
  CartesianGrid,
} from "recharts";
import type { BurndownReport } from "~/api/types";
import { chart } from "~/components/ui/chartTheme";

interface BurndownChartProps {
  report: BurndownReport;
}

export function BurndownChart({ report }: BurndownChartProps) {
  const data = report.points.map((p) => ({
    date: p.date.slice(5, 10),
    Remaining: p.remaining,
    Ideal: p.ideal,
  }));
  return (
    <div className="rounded-md border border-border bg-surface p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold">{report.cycle_name}</h3>
        <span
          className={
            report.is_on_track
              ? "rounded-full bg-status-done/10 px-2 py-0.5 text-[10px] font-medium text-status-done"
              : "rounded-full bg-priority-urgent/10 px-2 py-0.5 text-[10px] font-medium text-priority-urgent"
          }
        >
          {report.is_on_track ? "On track" : "Off track"}
        </span>
      </div>
      <ResponsiveContainer width="100%" height={220}>
        <LineChart data={data}>
          <CartesianGrid stroke={chart.grid} strokeDasharray="3 3" />
          <XAxis dataKey="date" stroke={chart.axis} fontSize={10} />
          <YAxis stroke={chart.axis} fontSize={10} />
          <Tooltip
            contentStyle={chart.tooltip}
          />
          <Legend wrapperStyle={{ fontSize: 11 }} />
          <Line type="monotone" dataKey="Remaining" stroke={chart.primary} strokeWidth={2} dot={false} />
          <Line
            type="monotone"
            dataKey="Ideal"
            stroke={chart.axis}
            strokeDasharray="5 5"
            strokeWidth={1}
            dot={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
