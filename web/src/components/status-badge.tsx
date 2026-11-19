import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import {
  deviceStatusLabel,
  deviceStatusTone,
  sessionStatusLabel,
  sessionStatusTone,
  taskStatusLabel,
  taskStatusTone,
  toneBadge,
  toneDot,
} from "@/lib/status";
import type { DeviceStatus, SessionStatus, TaskStatus } from "@/data/types";


export function StatusBadge({
  label,
  tone,
  className,
  pulse = false,
}: {
  label: string;
  tone: keyof typeof toneBadge;
  className?: string;
  pulse?: boolean;
}) {
  return (
    <Badge
      variant="outline"
      className={cn("gap-1.5 border font-medium", toneBadge[tone], className)}
    >
      <span className="relative flex size-1.5">
        {pulse ? (
          <span
            className={cn(
              "absolute inline-flex size-full animate-ping rounded-full opacity-60",
              toneDot[tone]
            )}
          />
        ) : null}
        <span className={cn("relative inline-flex size-1.5 rounded-full", toneDot[tone])} />
      </span>
      {label}
    </Badge>
  );
}

export function TaskStatusBadge({ status }: { status: TaskStatus }) {
  return (
    <StatusBadge
      label={taskStatusLabel[status]}
      tone={taskStatusTone[status]}
      pulse={status === "running"}
    />
  );
}

export function DeviceStatusBadge({ status }: { status: DeviceStatus }) {
  return (
    <StatusBadge
      label={deviceStatusLabel[status]}
      tone={deviceStatusTone[status]}
      pulse={status === "online"}
    />
  );
}

export function SessionStatusBadge({ status }: { status: SessionStatus }) {
  return (
    <StatusBadge
      label={sessionStatusLabel[status]}
      tone={sessionStatusTone[status]}
      pulse={status === "running" || status === "waiting_permission"}
    />
  );
}
