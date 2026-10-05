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
import { chart } from "~/components/ui/chartTheme";

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
        <CartesianGrid stroke={chart.grid} strokeDasharray="3 3" />
        <XAxis dataKey="name" stroke={chart.axis} fontSize={10} />
        <YAxis stroke={chart.axis} fontSize={10} />
        <Tooltip
          contentStyle={chart.tooltip}
        />
        <Bar dataKey="Completed" stackId="a" fill={chart.positive} />
        <Bar dataKey="Remaining" stackId="a" fill={chart.rest} />
      </BarChart>
    </ResponsiveContainer>
  );
}
