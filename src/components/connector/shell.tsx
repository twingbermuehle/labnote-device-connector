import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { connector } from "./sample-data";
import { StatusDot } from "./status";

export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="border-b border-border">
        <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-4 px-6 py-5">
          <div className="flex items-center gap-3">
            <StatusDot status={connector.status} />
            <div>
              <h1 className="text-sm font-semibold tracking-tight">LabNote Device Connector</h1>
              <p className="font-mono text-[0.7rem] text-muted-foreground">
                {connector.name} · {connector.location}
              </p>
            </div>
          </div>
          <nav className="flex items-center gap-1 font-mono text-xs">
            <NavLink to="/">Status</NavLink>
            <NavLink to="/setup">Setup</NavLink>
          </nav>
        </div>
      </header>

      <div className="mx-auto max-w-5xl px-6 py-8">
        <p className="mb-6 rounded-lg border border-border bg-muted px-4 py-3 text-xs text-muted-foreground">
          Design preview of the screens the connector shows on the lab computer at
          <span className="font-mono text-foreground"> 127.0.0.1:8420</span>. The values below are
          examples.
        </p>
        <div className="grid gap-4">{children}</div>
      </div>
    </div>
  );
}

function NavLink({ to, children }: { to: "/" | "/setup"; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="rounded-md px-3 py-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      activeProps={{ className: "bg-accent text-foreground" }}
      activeOptions={{ exact: true }}
    >
      {children}
    </Link>
  );
}
