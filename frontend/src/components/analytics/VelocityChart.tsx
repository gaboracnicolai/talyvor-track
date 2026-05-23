import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from "recharts";
import type { CycleVelocity } from "~/api/types";

interface VelocityChartProps {
  cycles: CycleVelocity[];
}

export function VelocityChart({ cycles }: VelocityChartProps) {
  const data = [...cycles]
    .sort((a, b) => new Date(a.start_date).getTime() - new Date(b.start_date).getTime())
    .map((c) => ({
      name: c.cycle_name,
      Completed: c.completed,
      Remaining: Math.max(0, c.total - c.completed),
    }));
  return (
    <ResponsiveContainer width="100%" height={240}>
      <BarChart data={data}>
        <CartesianGrid stroke="#1d2230" strokeDasharray="3 3" />
        <XAxis dataKey="name" stroke="#7a8294" fontSize={10} />
        <YAxis stroke="#7a8294" fontSize={10} />
        <Tooltip
          contentStyle={{
            backgroundColor: "#13161c",
            border: "1px solid #1d2230",
            fontSize: 12,
          }}
        />
        <Bar dataKey="Completed" stackId="a" fill="#22c55e" />
        <Bar dataKey="Remaining" stackId="a" fill="#3d4250" />
      </BarChart>
    </ResponsiveContainer>
  );
}
