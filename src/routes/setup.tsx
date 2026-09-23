import { createFileRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { Shell } from "@/components/connector/shell";
import { StatusPill } from "@/components/connector/status";
import {
  connector,
  devices,
  deviceChoices,
  discovered,
  parameters,
  profileOptions,
  readingModes,
} from "@/components/connector/sample-data";

export const Route = createFileRoute("/setup")({
  head: () => ({
    meta: [
      { title: "Connector setup — LabNote Device Connector" },
      {
        name: "description",
        content:
          "Setup screens of the LabNote Device Connector: LabNote address and ingest key, connector identity, client certificate fingerprint and instrument configuration.",
      },
      { property: "og:title", content: "Connector setup — LabNote Device Connector" },
      {
        property: "og:description",
        content:
          "Setup screens of the LabNote Device Connector: LabNote address and ingest key, connector identity, client certificate fingerprint and instrument configuration.",
      },
      { property: "og:type", content: "website" },
      { name: "twitter:card", content: "summary_large_image" },
    ],
  }),
  component: Setup,
});

function Setup() {
  return (
    <Shell>
      <Step index="1" title="Connection to LabNote">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="LabNote address" value={connector.labnoteUrl} />
          <Field label="Ingest API key" value="•••••••••••••••••••• stored" mono />
          <Field label="Connector name" value={connector.name} />
          <Field label="Location" value={connector.location} />
        </div>
        <p className="mt-4 text-xs text-muted-foreground">
          The key is kept in this computer's credential store. It is never written to a file and
          never appears in the logs.
        </p>
        <Button>Save and test</Button>
      </Step>

      <Step index="2" title="Client certificate">
        <p className="text-sm text-muted-foreground">
          Trust this fingerprint on every instrument so it accepts the connector:
        </p>
        <code className="mt-3 block break-all rounded-lg border border-border bg-muted px-4 py-3 font-mono text-[0.7rem] text-primary">
          {connector.clientFingerprint}
        </code>
      </Step>

      <Step index="3" title="Instruments">
        <ul className="divide-y divide-border rounded-lg border border-border">
          {devices.map((d) => (
            <li key={d.externalDeviceId} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <span className="text-sm font-medium">{d.name}</span>
              <span className="font-mono text-[0.7rem] text-muted-foreground">
                {d.externalDeviceId}
              </span>
              <StatusPill status={d.status} />
              {d.security ? (
                <span className="text-xs text-muted-foreground">
                  {d.security}
                  {d.certificateUntil ? ` · certificate valid until ${d.certificateUntil}` : ""}
                </span>
              ) : null}
              <span className="ml-auto flex gap-2">
                {d.status === "error" ? (
                  <Button small>Trust certificate</Button>
                ) : null}
                <Button small variant="ghost">
                  Edit
                </Button>
                <Button small variant="danger">
                  Remove
                </Button>
              </span>
            </li>
          ))}
        </ul>

        <h3 className="mt-6 text-sm font-semibold">Find instruments</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          Searches this network for OPC UA / LADS instruments and fills in their address.
        </p>
        <ul className="mt-3 divide-y divide-border rounded-lg border border-border">
          {discovered.map((s) => (
            <li key={s.endpoint} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <span className="text-sm font-medium">{s.name}</span>
              <span className="font-mono text-[0.7rem] text-muted-foreground">{s.endpoint}</span>
              {s.note ? (
                <span className="text-xs text-muted-foreground">{s.note}</span>
              ) : null}
              <span className="ml-auto">
                {s.added ? (
                  <span className="text-xs text-muted-foreground">already added</span>
                ) : (
                  <Button small variant="ghost">
                    Use this
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
        <div className="mt-3">
          <Button variant="ghost">Search for instruments</Button>
        </div>

        <h3 className="mt-6 text-sm font-semibold">Add an instrument</h3>
        <div className="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Field label="Friendly name" value="HPLC 07" />
          <Field label="Device ID" value="HPLC-07" mono />
          <Field label="OPC UA endpoint" value="opc.tcp://192.168.1.50:4840" mono />
          <Field label="OPC UA user name (empty = certificate login)" value="labnote" />
          <Field label="OPC UA password" value="•••••••••• stored" mono />
          <Field label="Manufacturer" value="agilent" />
          <Field label="Model" value="1260 Infinity II" />
          <Field label="Instrument type" value="hplc" />
          <Field label="Instrument node" value="ns=2;i=5001" mono />
          <Field label="Mapping profile" value={profileOptions[0]!.label} />
          <Field label="How this instrument is read" value={readingModes[0]!.label} />
          <Field label="Value that marks a new measurement" value="RegisteredWeight" mono />
          <Field label="Default units" value="min / mAU" mono />
        </div>
        <label className="mt-4 flex items-start gap-3 text-sm">
          <input type="checkbox" className="mt-0.5 size-4 accent-primary" />
          Allow a signed but unencrypted connection if the instrument offers nothing better
        </label>
        <h3 className="mt-6 text-sm font-semibold">Which device on this instrument?</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          Shown when one instrument hosts more than one device, so the right one is reported.
        </p>
        <ul className="mt-3 space-y-2">
          {deviceChoices.map((d, i) => (
            <li key={d.nodeId} className="flex items-center gap-3 text-sm">
              <input
                type="radio"
                name="device-choice"
                defaultChecked={i === 0}
                className="size-4 accent-primary"
              />
              <span>{d.label}</span>
              <span className="font-mono text-[0.7rem] text-muted-foreground">{d.nodeId}</span>
            </li>
          ))}
        </ul>
        <h3 className="mt-6 text-sm font-semibold">Measurable parameters</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          Detected on the instrument. Ticked parameters are sent to LabNote.
        </p>
        <ul className="mt-3 space-y-2">
          {parameters.map((p) => (
            <li key={p.name} className="flex items-center gap-3 text-sm">
              <input
                type="checkbox"
                defaultChecked={p.enabled}
                disabled={p.kind === "expected"}
                className="size-4 accent-primary"
              />
              <span>{p.name}</span>
              {p.unit ? (
                <span className="font-mono text-[0.7rem] text-muted-foreground">{p.unit}</span>
              ) : null}
              <span className="text-xs text-muted-foreground">
                {p.kind === "series"
                  ? "curve"
                  : p.kind === "value"
                    ? "single value"
                    : "appears after the first measurement"}
              </span>
            </li>
          ))}
        </ul>
        <div className="mt-3">
          <Button variant="ghost">Detect parameters</Button>
        </div>

        <p className="mt-4 text-xs text-muted-foreground">
          The connection uses the strongest encryption the instrument offers (Basic256Sha256,
          Aes128 or Aes256). Leave the user name empty to log in with the connector certificate.
          Instruments that only offer unencrypted or unauthenticated access are refused.
        </p>
        <div className="mt-4 flex flex-wrap gap-2">
          <Button variant="ghost">Test connection</Button>
          <Button>Save instrument</Button>
        </div>
      </Step>

      <Step index="4" title="Updates">
        <label className="flex items-center gap-3 text-sm">
          <input type="checkbox" defaultChecked className="size-4 accent-primary" />
          Install updates automatically
        </label>
        <p className="mt-2 text-xs text-muted-foreground">
          Turn this off in validated environments — the connector then only reports that a newer
          version exists.
        </p>
      </Step>
    </Shell>
  );
}

function Step({ index, title, children }: { index: string; title: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border border-border bg-card p-5">
      <h2 className="mb-4 flex items-center gap-3 font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
        <span className="grid size-6 place-items-center rounded-md border border-border text-foreground">
          {index}
        </span>
        {title}
      </h2>
      {children}
    </section>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <p className="text-[0.7rem] uppercase tracking-[0.12em] text-muted-foreground">{label}</p>
      <p
        className={`mt-1 truncate rounded-md border border-border bg-muted px-3 py-2 text-sm ${
          mono ? "font-mono text-xs" : ""
        }`}
      >
        {value}
      </p>
    </div>
  );
}

function Button({
  children,
  small,
  variant = "primary",
}: {
  children: ReactNode;
  small?: boolean;
  variant?: "primary" | "ghost" | "danger";
}) {
  const base = small ? "px-2.5 py-1 text-xs" : "mt-5 px-4 py-2 text-sm";
  const tone =
    variant === "primary"
      ? "bg-primary text-primary-foreground hover:bg-primary/90"
      : variant === "danger"
        ? "border border-destructive/50 text-destructive hover:bg-destructive/10"
        : "border border-border text-foreground hover:bg-accent";
  return (
    <button className={`rounded-md font-medium transition-colors ${base} ${tone}`}>
      {children}
    </button>
  );
}
