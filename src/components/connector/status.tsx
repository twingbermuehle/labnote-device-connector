import type { DeviceStatus } from "./sample-data";

const tone: Record<string, string> = {
  connected: "text-success border-success/40 bg-success/10",
  online: "text-success border-success/40 bg-success/10",
  degraded: "text-warning border-warning/40 bg-warning/10",
  disconnected: "text-destructive border-destructive/40 bg-destructive/10",
  error: "text-destructive border-destructive/40 bg-destructive/10",
  offline: "text-destructive border-destructive/40 bg-destructive/10",
  unknown: "text-muted-foreground border-border bg-muted",
};

const dot: Record<string, string> = {
  connected: "bg-primary",
  online: "bg-primary",
  degraded: "bg-warning",
  disconnected: "bg-destructive",
  error: "bg-destructive",
  offline: "bg-destructive",
  unknown: "bg-muted-foreground",
};

export function StatusPill({ status }: { status: DeviceStatus | string }) {
  return (
    <span
      className={`rounded-full border px-2 py-0.5 font-mono text-[0.65rem] uppercase tracking-[0.12em] ${
        tone[status] ?? tone['unknown']
      }`}
    >
      {status}
    </span>
  );
}

export function StatusDot({ status }: { status: string }) {
  return (
    <span
      aria-hidden
      className={`inline-block size-2.5 shrink-0 rounded-full ${dot[status] ?? dot['unknown']}`}
    />
  );
}
