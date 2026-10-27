# Fix: scale is reachable but the connector shows "error"

## What we know
- UAExpert can reach the scale at `opc.tcp://172.18.30.101:4840`, but the connector marks Balance-01 as "error" and its network search finds nothing.
- The real error text has not been captured yet, so we don't know the cause yet.

## Steps
1. **Capture the real cause.** Show the full error for each instrument right in the instrument row (today it only shows "error"), and add a "Copy diagnostics" button that copies the last error, the security mode, the login method and the connector version.
2. **Check likely causes against the error text:**
   - The scale's certificate changed (after a restart or firmware update) and is no longer trusted. If so, show the "trust this certificate" prompt again instead of failing silently.
   - The username login is rejected after a scale restart, or the saved password was lost from the keychain. If so, show a clear message asking for the password again.
   - The scale allows only one OPC UA session, so an open UAExpert session blocks the connector. If so, say "close other OPC UA clients (e.g. UAExpert)" in the message.
   - The connector stays stuck in retry backoff after a network drop. If so, cap reconnect delays at 30 s and add a "Reconnect now" button.
3. **Network search:** if the search finds nothing but a configured instrument answers, say "Search found nothing, but configured instruments are reachable" instead of the red error. The search only scans the local /24 network, and the scale may sit on another subnet.
4. **Test and release** as v1.7.3: run the simulator acceptance workflow, publish, and check all downloads.

## What you can do now
Close UAExpert, then on the setup page click **Edit → Save** for Balance-01. That forces a reconnect. If it still shows "error", send the "Last error" text from "4. Status".

## Technical notes
- `internal/state/state.go`: expose a per-device `LastError`. In `internal/webui/static/app.js`, render it under the status badge.
- `internal/device/supervisor.go`: classify `BadTooManySessions`, `BadCertificateUntrusted`, `BadIdentityTokenRejected` and timeouts into plain messages, and add a backoff cap plus a manual reconnect endpoint.
- `internal/discovery`: show a softer message when configured endpoints are healthy.
