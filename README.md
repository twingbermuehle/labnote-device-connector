# LabNote Device Connector

A self-contained on-premises agent that runs on a small PC or VM inside a
customer's lab network. It connects to laboratory instruments that speak OPC UA
with the LADS companion specification, subscribes to their results, and pushes
every finished measurement to LabNote's existing REST ingest API.

**Outbound HTTPS only — no inbound firewall ports, no VPN.**

This repository has no database and no Supabase connection. The agent only
talks to LabNote's REST API with a per-organisation ingest API key.

## What is in here

| Path | Purpose |
| --- | --- |
| `cmd/connector` | the shipped binary |
| `internal/device` | OPC UA session, subscriptions, reconnect |
| `internal/lads` | LADS information-model browsing and value decoding |
| `internal/mapping` | finished LADS result to one LabNote ingest record |
| `internal/outbox` | SQLite store-and-forward queue (WAL, pure Go) |
| `internal/uploader` | drains the outbox in order with retries |
| `internal/heartbeat` | 60 s connector + device report |
| `internal/webui` | local setup and monitoring interface |
| `internal/profiles` | vendor mapping profiles (YAML) |
| `internal/updater` | signed self-update against GitHub Releases |
| `deploy/` | Windows service installer, systemd unit, Docker image |
| `src/` | web preview of the setup screens (design review only) |

## Install

**Windows** — download `labnote-connector_windows_installer.zip` from the
release, then in an elevated PowerShell:

```powershell
.\install.ps1 -Binary .\labnote-connector_windows_amd64.exe
```

**Linux** — download `labnote-connector_linux_amd64.tar.gz`, then:

```bash
sudo ./deploy/systemd/install.sh ./labnote-connector_linux_amd64
```

**Docker**

```bash
docker run -d --name labnote-connector \
  -p 127.0.0.1:8420:8420 \
  -v labnote-connector-data:/data \
  -e LABNOTE_INGEST_API_KEY=... \
  ghcr.io/labnote/labnote-device-connector:latest
```

Then open <http://127.0.0.1:8420> and complete the setup.

## Setup

1. **LabNote address and ingest API key.** The key is stored in the operating
   system credential store (Windows Credential Manager, macOS Keychain, or the
   freedesktop Secret Service). It is never written to a config file and never
   logged. In the container image there is no credential store, so the key is
   read from `LABNOTE_INGEST_API_KEY` instead.
2. **Connector name and location.** Reported in every heartbeat.
3. **Client certificate.** Generated per installation on first start. Its
   SHA-256 fingerprint is shown in the setup screen so it can be trusted on
   each instrument.
4. **Instruments.** Endpoint (`opc.tcp://host:4840`), friendly name, device ID,
   LADS device node and mapping profile. Security is fixed to Basic256Sha256 /
   SignAndEncrypt with mutual certificates — endpoints that only offer `None`
   or anonymous authentication are refused.
5. **Trust each instrument certificate.** On first connect the connector pins
   the instrument's server certificate (trust on first use) and asks for
   confirmation in the setup screen. If a pinned certificate later changes, the
   connection is refused until the new fingerprint is confirmed again — that is
   the rotation flow.

## What the connector sends

Heartbeat, every 60 seconds:

```
POST {labnote_url}/functions/v1/api-v1/v1/connectors
Authorization: Bearer <org ingest API key>
```

One record per finished result:

```
POST {labnote_url}/functions/v1/api-v1/v1/results
Authorization: Bearer <org ingest API key>
```

`external_result_id` is the OPC UA NodeId plus the result's source timestamp.
It is the idempotency key: replays after a reconnect are rejected server-side,
and the local outbox refuses to queue the same id twice.

`sample_code` is set whenever the instrument reported a barcode or sample ID,
which is what lets LabNote file the result on the right sample automatically.
The untouched LADS metadata travels along under `lads` for audit
reproducibility.

## Store and forward

Every result is written to the local SQLite outbox **before** any upload is
attempted. The upload worker drains the queue strictly in order, retries with
backoff for up to 24 hours, and stops the batch at the first retryable failure
so ordering is preserved. Delivered rows are kept 7 days. The current queue
size is reported as `queue_depth` in the heartbeat, so LabNote's connector card
shows backpressure.

## Vendor profiles

A profile is a YAML file that says where sample ID, method name and operator
live in a given vendor's LADS tree. `internal/profiles/builtin/generic-lads.yaml`
ships with the binary and targets the SPECTARIS LADS reference server. Drop
additional `*.yaml` files into `<data-dir>/profiles/` to add instruments
without rebuilding.

## Security

- Outbound HTTPS to the LabNote instance only. TLS verification is always on.
- The only listening socket is the setup interface on `127.0.0.1:8420`;
  non-loopback requests are rejected.
- Read-only on instruments: no Method calls, no Writes. Write-back is out of
  scope for v1.
- No telemetry. Logs are local, rotating, and contain metadata only — never
  result payloads.

## Testing without hardware

```bash
docker compose -f docker-compose.simulator.yml up -d
go test ./...                                  # unit tests
go test -tags acceptance ./test/acceptance/... -v
```

The acceptance tests cover: one finished result yields exactly one ingest
record with correct units and timestamps; results produced during a network
outage all arrive in order with zero duplicates; an untrusted instrument
certificate is refused with a clear message; a barcode result carries
`sample_code`.

## Setup-screen preview

`src/` holds a web preview of the setup wizard and dashboard for design review
(`bun install && bun run dev`). It uses sample data only, has no backend, and
is not what ships — the binary serves its own embedded copy from
`internal/webui/static`.

## Releasing

Push a version tag:

```bash
git tag v1.0.0 && git push origin v1.0.0
```

The workflow builds the Windows, Linux amd64 and Linux arm64 binaries plus the
multi-arch container image, generates an SPDX SBOM, produces checksums, signs
every artifact with cosign (keyless), and publishes a GitHub Release.

Afterwards, update the three download URLs in the main LabNote app
(`src/modules/instruments/connectorRelease.ts`) so the in-app download button,
the update badge and the public `/connector` page point at the real files.

## Out of scope (v1)

- Write-back or remote control of instruments.
- Non-OPC-UA protocols (file-drop ingestion already exists server-side).
- Multi-tenancy: one connector serves exactly one LabNote organisation.
