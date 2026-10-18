"use client";

import {
  ResponsiveContainer,
  LineChart,
  Line,
  YAxis,
  Tooltip,
} from "recharts";
import type { FleetSample } from "@/lib/ws";

export function Sparkline({
  data,
  color = "#10b981",
  max = 100,
  height = 36,
}: {
  data: FleetSample[];
  color?: string;
  max?: number;
  height?: number;
}) {
  if (data.length < 2) {
    return (
      <div
        style={{ height }}
        className="flex items-center text-xs text-muted-foreground"
      >
        collecting…
      </div>
    );
  }
  return (
    <div style={{ height }} className="w-full">
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 2, right: 0, bottom: 2, left: 0 }}>
          <YAxis domain={[0, max]} hide />
          <Tooltip
            wrapperClassName="!text-xs"
            labelFormatter={() => ""}
            formatter={(v) => [`${Number(v).toFixed(1)}`, ""]}
          />
          <Line
            type="monotone"
            dataKey="v"
            stroke={color}
            strokeWidth={1.5}
            dot={false}
            isAnimationActive={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
