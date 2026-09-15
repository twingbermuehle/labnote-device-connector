# LabNote Device Connector

Build the on-premises connector agent that reads lab instrument results over OPC UA (LADS) and pushes every finished measurement to LabNote's existing REST ingest API. Outbound HTTPS only, no inbound ports, no database in this project.

Two things get built here:

1. **The shipped agent** — a single Go binary with an embedded local setup interface, written as source in this workspace and compiled into real downloads by an automated release workflow after this project is connected to GitHub.
2. **A preview version of the setup screens** — the same wizard and dashboard rendered as web pages in this workspace, so the layout and wording can be reviewed and approved here before being embedded into the binary.

Note on the preview: this workspace can't run or compile Go, so the live preview shows only the setup-screen mockup. Real Windows/Linux/Docker downloads appear once the project is connected to GitHub and a version tag is pushed.

## What the agent does

**Setup wizard (local only, reachable at 127.0.0.1:8420)**
- LabNote instance address and the organisation's ingest key, entered once and stored in the operating system's credential store, never in a file.
- Connector name and location.
- Add, edit and remove instruments: address, encrypted-and-signed security only, certificate choice, friendly name, and a Test connection button that browses the device and reports what it found.

**Instrument connection**
- A per-installation client certificate is generated and its fingerprint shown, so it can be trusted on each instrument.
- Each instrument's certificate is pinned on first connect with an explicit confirmation, plus a documented replacement flow.
- Automatic reconnect with growing backoff up to 5 minutes, subscription keep-alives, per-device status.
- Browses the LADS structure, finds result and program objects, and subscribes to result changes rather than polling.
- Vendor mapping profiles say where sample ID, method and operator live; the shipped profile targets the generic/base LADS reference simulator.

**Turning a result into a LabNote record**
- One finished result becomes one uploaded record: device identifier, a stable result identifier (node + timestamp) so repeats are rejected, method, measurement time, sample code when the instrument reported a barcode, numeric series with units taken from the instrument, a summary of peaks and scalar values, and the untouched raw LADS data for audit.

**Heartbeat**
- Every 60 seconds the connector reports name, location, version, status, pending-queue size, last error, and the full device list, so LabNote's status card and update badge stay current.

**Never lose a result**
- Every result is written to a local queue before any upload attempt; a worker drains it in order with retries up to 24 hours, keeps failures, and deletes delivered rows after 7 days. The queue size is reported in the heartbeat.

**Security**
- Outbound HTTPS only; the only listening port is the local setup interface. Read-only on instruments — no commands, no writes. Encrypted-and-signed sessions required; unauthenticated or unencrypted endpoints refused. Keys in the credential store, certificate checks always on, no telemetry, rotating local logs with metadata only.

**Monitoring and updates**
- Local dashboard: connector status, per-device status, last result per device, queue depth, last error, and a log-export button.
- Update check against the published releases feed, with signature verification before swapping, and an option to disable it for validated environments.

**Testing without hardware**
- A compose file runs the open LADS reference simulator, with acceptance tests for: one finished result yields exactly one record with correct units and times; a 10-minute network outage delivers everything in order with no duplicates afterwards; an untrusted instrument certificate is refused with a clear message; a barcode result carries the sample code.

**Packaging**
- Windows installer running as a service, Linux binary plus service unit, Docker image; the release workflow builds all of them on a version tag and attaches checksums, a software bill of materials, and signatures.

Out of scope for this version: writing back to or controlling instruments, non-OPC-UA protocols, and serving more than one organisation.

## Technical detail

- Go 1.23+, `github.com/gopcua/opcua`, `modernc.org/sqlite` (pure Go, WAL) for the outbox, embedded setup UI via `embed`, loopback listener on 127.0.0.1:8420.
- Layout: `cmd/connector`, `internal/{config,keychain,opcua,lads,profiles,mapping,outbox,uploader,heartbeat,updater,webui}`, `profiles/*.yaml`, `deploy/{windows,systemd,docker}`, `.github/workflows/release.yml` (goreleaser-style matrix + syft SBOM + cosign signing), `docker-compose.simulator.yml`, `test/acceptance`.
- Endpoints consumed (already built server-side, not rebuilt here): `POST /functions/v1/api/v1/connectors`, `POST /functions/v1/api/v1/results`, bearer org ingest key.
- The web preview of the setup screens is added as TanStack routes (`/` wizard-and-dashboard mock, driven by local sample state only). No Lovable Cloud, no Supabase client, no keys in this repo.
- After the first release: replace the three placeholder download URLs in the main LabNote app's `src/modules/instruments/connectorRelease.ts`.
