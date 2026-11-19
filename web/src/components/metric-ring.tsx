import { cn } from "@/lib/utils";
import { formatPercent } from "@/lib/status";

interface MetricRingProps {
  label: string;
  value: number | null;
  detail?: string;
  size?: number;
  stale?: boolean;
  className?: string;
}

function colorClass(value: number, stale: boolean) {
  if (stale) return "stroke-muted-foreground/40";
  if (value >= 95) return "stroke-rose-500";
  if (value >= 80) return "stroke-amber-500";
  return "stroke-emerald-500";
}

export function MetricRing({
  label,
  value,
  detail,
  size = 68,
  stale = false,
  className,
}: MetricRingProps) {
  const stroke = Math.max(5, Math.round(size / 11));
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const pct = value === null ? 0 : Math.min(100, Math.max(0, value));
  const dash = (pct / 100) * circumference;
  const unavailable = value === null;

  return (
    <div className={cn("flex flex-col items-center gap-1.5", className)}>
      <div className="relative" style={{ width: size, height: size }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="-rotate-90">
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            strokeWidth={stroke}
            className={cn("stroke-muted", unavailable && "opacity-60")}
            strokeDasharray={unavailable ? "2 5" : undefined}
          />
          {!unavailable && (
            <circle
              cx={size / 2}
              cy={size / 2}
              r={radius}
              fill="none"
              strokeWidth={stroke}
              strokeLinecap="round"
              strokeDasharray={`${dash} ${circumference}`}
              className={cn("transition-[stroke-dasharray] duration-700", colorClass(pct, stale))}
              opacity={stale ? 0.6 : 1}
            />
          )}
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span
            className={cn(
              "text-sm font-semibold tabular-nums tracking-tight",
              stale && "text-muted-foreground"
            )}
          >
            {unavailable ? "—" : `${formatPercent(pct)}%`}
          </span>
        </div>
      </div>
      <div className="text-center leading-tight">
        <div className="text-[11px] font-medium text-muted-foreground">{label}</div>
        {detail ? (
          <div className="text-[10px] tabular-nums text-muted-foreground/70">{detail}</div>
        ) : null}
      </div>
    </div>
  );
}
