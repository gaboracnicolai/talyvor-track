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
          <Legend wrapperStyle={{ fontSize: 11 }} />
          <Line type="monotone" dataKey="Remaining" stroke="#f0a030" strokeWidth={2} dot={false} />
          <Line
            type="monotone"
            dataKey="Ideal"
            stroke="#7a8294"
            strokeDasharray="5 5"
            strokeWidth={1}
            dot={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
