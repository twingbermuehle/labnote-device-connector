import { createFileRoute, Link } from "@tanstack/react-router";

import { Shell } from "@/components/connector/shell";
import { StatusPill, StatusDot } from "@/components/connector/status";
import { devices, connector, queue } from "@/components/connector/sample-data";

export const Route = createFileRoute("/")({
  head: () => ({
    meta: [
      { title: "LabNote Device Connector — instrument status" },
      {
        name: "description",
        content:
          "Local monitoring screen of the LabNote Device Connector: instrument connections, last results received, upload queue and errors.",
      },
      { property: "og:title", content: "LabNote Device Connector — instrument status" },
      {
        property: "og:description",
        content:
          "Local monitoring screen of the LabNote Device Connector: instrument connections, last results received, upload queue and errors.",
      },
      { property: "og:type", content: "website" },
      { name: "twitter:card", content: "summary_large_image" },
    ],
  }),
  component: Dashboard,
});

function Dashboard() {
  return (
    <Shell>
      <section className="grid gap-4 sm:grid-cols-3">
        <Metric label="Connector" value={connector.status} accent>
          <StatusDot status={connector.status} />
        </Metric>
        <Metric label="Waiting to upload" value={`${queue.depth} results`} />
        <Metric label="Version" value={connector.version} note={connector.updateNote} />
      </section>

      <section className="rounded-xl border border-border bg-card">
        <header className="flex flex-wrap items-baseline justify-between gap-2 border-b border-border px-5 py-4">
          <h2 className="font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
            Instruments
          </h2>
          <Link
            to="/setup"
            className="text-xs font-medium text-primary underline-offset-4 hover:underline"
          >
            Add or edit instruments
          </Link>
        </header>

        <ul className="divide-y divide-border">
          {devices.map((d) => (
            <li key={d.externalDeviceId} className="grid gap-3 px-5 py-4 sm:grid-cols-[1fr_auto]">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="text-sm font-semibold text-foreground">{d.name}</h3>
                  <span className="font-mono text-[0.7rem] text-muted-foreground">
                    {d.externalDeviceId}
                  </span>
                  <StatusPill status={d.status} />
                </div>
                <p className="mt-1 truncate font-mono text-[0.7rem] text-muted-foreground">
                  {d.endpoint}
                </p>
                {d.message ? (
                  <p className="mt-2 text-xs text-destructive">{d.message}</p>
                ) : null}
              </div>
              <dl className="grid shrink-0 grid-cols-2 gap-x-6 gap-y-1 text-xs sm:text-right">
                <dt className="text-muted-foreground">Last result</dt>
                <dd className="font-mono text-foreground">{d.lastResult}</dd>
                <dt className="text-muted-foreground">Method</dt>
                <dd className="font-mono text-foreground">{d.method}</dd>
              </dl>
            </li>
          ))}
        </ul>
      </section>

      <section className="grid gap-4 sm:grid-cols-2">
        <div className="rounded-xl border border-border bg-card p-5">
          <h2 className="font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
            Upload queue
          </h2>
          <p className="mt-3 text-sm text-foreground">
            {queue.depth === 0
              ? "Everything the instruments reported has reached LabNote."
              : `${queue.depth} results are stored locally and will be sent in order as soon as LabNote is reachable.`}
          </p>
          <p className="mt-2 text-xs text-muted-foreground">
            Nothing is deleted before LabNote confirms it. Retries continue for up to 24 hours.
          </p>
        </div>
        <div className="rounded-xl border border-border bg-card p-5">
          <h2 className="font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
            Last error
          </h2>
          <p className="mt-3 text-sm text-foreground">{connector.lastError ?? "None."}</p>
          <button className="mt-4 rounded-md border border-border px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-accent">
            Export logs
          </button>
        </div>
      </section>
    </Shell>
  );
}

function Metric({
  label,
  value,
  note,
  accent,
  children,
}: {
  label: string;
  value: string;
  note?: string;
  accent?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div className="rounded-xl border border-border bg-card p-5">
      <p className="font-mono text-[0.7rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </p>
      <p
        className={`mt-2 flex items-center gap-2 text-lg font-semibold ${
          accent ? "text-primary" : "text-foreground"
        }`}
      >
        {children}
        {value}
      </p>
      {note ? <p className="mt-1 text-xs text-muted-foreground">{note}</p> : null}
    </div>
  );
}
