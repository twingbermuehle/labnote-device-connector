# Verification of guide, LabNote connection, and program

Honest answer first: I cannot confirm "100% correct". The program and the LabNote connection look correct in the code, but I found three concrete errors in the setup guide, and two things that have never been proven outside this workspace. Below is what I checked, what is wrong, and what I would fix.

## What is confirmed correct

- The connector sends to exactly the required addresses: `/functions/v1/api-v1/v1/connectors` (heartbeat, and the setup test) and `/functions/v1/api-v1/v1/results` (measurements). No other address exists in the code.
- The key is sent as a bearer token, read fresh from the computer's credential store on every call, and the address must start with `https://` or the connector refuses it.
- Certificate handling matches the guide: the connector shows its own fingerprint, and the first contact with an instrument shows a "Trust certificate" confirmation before any data flows.
- Repeated deliveries cannot create duplicates; results wait in a local queue and are sent in order after an outage.
- All four instrument tests pass here against the free LADS reference instrument.

## Errors in the guide that need fixing

1. **Docker image name is wrong.** The guide says `ghcr.io/labnote/labnote-device-connector:latest`. The release actually publishes `ghcr.io/twingbermuehle/labnote-device-connector:latest`. Anyone following the guide gets "not found".
2. **The LabNote address example is misleading.** The guide shows `https://your-labnote-address.example.com`. Only the real LabNote service address works; anything else produces "not found" on every upload. The guide must name the exact address to enter.
3. **The ingest key step is still generic.** Step 4 says "create an ingest API key in LabNote" without naming the menu, so a lab user cannot follow it.

## Not yet proven (cannot be called 100%)

- The instrument tests have only run in this workspace, not on GitHub with the latest code. The last GitHub run used older code and failed.
- Nothing has been tested against a real instrument (including Sartorius balances) — only against the reference simulator.

## Proposed work

1. Correct the three guide errors above (Docker image name, real LabNote address, exact ingest-key location once you tell me the menu name).
2. Check that the Docker image is publicly downloadable, and if it is not, either make it public or remove Docker from the guide.
3. Re-run the instrument tests on GitHub with the current code and report the result plainly.
4. If green, publish v1.4.1 and hand the corrected download links to the main LabNote app.

## Technical detail

- `internal/labnote/client.go`: paths pinned as constants `PathConnectors`/`PathResults`; `newRequest` enforces `https://`, sets `Authorization: Bearer`, `Content-Type`, versioned `User-Agent`; `Error.Duplicate()` (409) counts as delivered, `Permanent()` covers 400/422/413, `Unauthorized()` 401/403.
- `internal/webui/{server.go,static/app.js}`: `POST /api/instruments/{id}/trust` plus the pending-trust button; client fingerprint surfaced from `client_certificate_fingerprint`.
- `.github/workflows/release.yml`: assets `labnote-connector_windows_installer.zip`, `labnote-connector_windows_amd64.exe`, `labnote-connector_linux_{amd64,arm64}.tar.gz`, `SHA256SUMS.txt`, cosign signatures; image tags `ghcr.io/${{ github.repository }}:{tag,latest}` — hence the wrong name in the guide.
