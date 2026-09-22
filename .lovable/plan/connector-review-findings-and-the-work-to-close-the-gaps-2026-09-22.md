# Connector review: findings and the work to close the gaps

Short answers to your two questions:

**Is the UX top level?** Not yet. The setup page works and covers the normal path, but it reads like an engineer's form, not a guided setup. No step progress, errors appear in one shared line per section instead of next to the field that is wrong, no spinners, browser-default pop-ups for irreversible actions, and the instrument table overflows on a narrow window.

**Does it work for every OPC UA / LADS device?** No. Today it reliably works for devices that look like the reference simulator. Several common, perfectly valid device behaviours are refused or silently missed. These are the real pilot risks.

---

## Part A — Device compatibility (highest risk first)

1. **Only one encryption method is accepted.** Every instrument is forced to `Basic256Sha256`. Devices that only offer the newer `Aes128Sha256RsaOaep` / `Aes256Sha256RsaPss` are reported as "offers no encrypted endpoint" and cannot be connected at all. Fix: accept all three, prefer the strongest the device offers, and show which one was used.
2. **Sign-only devices are invisible.** Devices that offer signed-but-not-encrypted sessions are skipped everywhere, including in the network search. Fix: keep encrypted as the default and preferred choice, allow signed-only per instrument with an explicit warning the operator has to accept.
3. **"Finished" is guessed from four English words.** A device that says `Ready`, `Done`, `Idle`, or reports completion through a state machine sub-state delivers results that are never picked up. Fix: also treat the standard state-machine completion states as finished, match on the state number as well as the text, and add a per-instrument fallback "treat a new stop time as finished".
4. **Event-only devices deliver nothing.** Result detection listens only for value changes; devices that announce a finished result through an event are silently ignored. Fix: also subscribe to events and rescan on them.
5. **Saved node references can break after an instrument restart.** The stored device node is reused verbatim; if the instrument reorders its namespaces on restart the reference points somewhere else or stops resolving, and it retries forever. Fix: store the namespace address alongside the node and re-resolve it on every connect, with a clear error and a re-detect button when it no longer exists.
6. **One monitored item per existing result.** On a device with a long result history this can exceed the device's own limit; the failures are only logged. Fix: watch the result set itself plus a bounded number of recent results, batch the requests, and surface "the instrument refused to watch some items" in the status area.
7. **Units are missed when the device only reports a unit code.** Devices that set only the numeric unit code (no display text) end up with an empty unit. Fix: add the standard unit-code table as a fallback.
8. **Multi-device and multi-part instruments are flattened.** The first device on a server is picked silently and results from different parts of one instrument are indistinguishable. Fix: a device picker in the UI when more than one is found, and record which part produced each result.
9. **Very large measurement curves are sent whole.** A spectrum with hundreds of thousands of points is stored and uploaded in one request with no limit. Fix: a configurable point cap with even down-sampling and a note on the record.
10. **Structured measurement values are not read.** Arrays wrapped in structured types (used by some array-sensor devices) fall through as unreadable. Fix: unwrap those before converting to numbers.
11. **Timestamps can silently fall back to "now".** If the device reports no measurement time the current time is used without any marker, which also makes the duplicate key unstable — the same physical result can be uploaded twice under different identities. Fix: derive the result identity from device-stable fields only, and flag an estimated time on the record.
12. **The connector's own certificate never rotates.** Generated once for five years, no warning, no renewal. Fix: show the expiry date, warn 90 days ahead, offer a one-click renewal. Also show the pinned instrument certificate's expiry so a planned renewal can be told apart from a suspicious change.

## Part B — Setup interface

- Turn the four sections into a guided flow with real done / pending / blocked markers, so it is obvious that LabNote must be connected before instruments can be added.
- Field-level validation and error messages directly under the field that is wrong, plus required markers.
- Real progress: spinners on every button, a cancel option for the network search, and buttons that disable while working so nothing can be double-submitted.
- Replace browser pop-ups for "Remove" and "Trust certificate" with proper dialogs that explain the consequence, and make status messages announced for screen readers.
- Copy-to-clipboard buttons for the fingerprints and the push address, a horizontally scrollable instrument table, visible focus outlines, and plain-language help on "mapping profile" and "device node".
- A first-run welcome state, and a persistent green confirmation on the LabNote step once it has been tested successfully.
- The React design preview has drifted from the real page (different labels, no push-instrument card, all buttons inert). Bring the wording in line so it stops being a misleading reference.

## Part C — Proving it

Extend the simulator test suite to cover the new ground: each encryption method, a signed-only device, a device with non-standard finished wording, an event-only device, a server restart that reorders namespaces, a multi-device server, a very large curve, and a unit-code-only unit. Then publish as v1.6.0 with updated setup guide, handoff, and endpoint checklist.

## Technical detail

- `internal/model`: add `SecurityPolicyAes128Sha256RsaOaep`, `SecurityPolicyAes256Sha256RsaPss`, `SecurityModeSign`; add `NamespaceURI`, `BrowsePath`, `FunctionalUnit`, `MaxPoints`, `AllowSignOnly` to `Instrument`; add `TimeEstimated` to `Result`.
- `internal/config/config.go:177-185`: replace the single-policy check with an allow-list and policy ranking; keep `None`/anonymous refused.
- `internal/device/supervisor.go`: `selectSecureEndpoint` ranks by policy strength and mode; `dial` re-resolves the device node through the server `NamespaceArray` (i=2255); add an `EventNotificationList` branch next to the `DataChangeNotification` branch at :209; bound and batch `sub.Monitor` calls at :165-177 and record refusals in `state`; set explicit `SubscriptionParameters` (lifetime, keep-alive, max notifications) instead of gopcua defaults.
- `internal/lads`: state-machine aware finished detection (`CurrentState.Id`/`StateNumber` plus text), `EUInformation.UnitId` UNECE fallback in `variant.go:117-127`, `ExtensionObject` unwrapping in `VariantToFloats`, functional-unit tagging in `ResultSetNodes`, `ResultReadyEvent` subscription helper.
- `internal/mapping/mapping.go:28-46`: stable `ExternalResultID` from node identity + device-reported sequence/stop time only (never `time.Now()`); `TimeEstimated` flag; `MaxPoints` down-sampling in `zip()`.
- `internal/certs/certs.go`: expiry inspection + `RenewClientCert`, surfaced through `/api/status`.
- `internal/webui/static/*`: step state machine, per-field errors with `aria-describedby`, `aria-live` hints, dialog component, clipboard buttons, `:focus-visible` styles, table scroll wrapper, device picker from `TestReport.Devices`.
- `test/acceptance`: new cases per Part C; simulator compose gains a second device and a sign-only endpoint.
