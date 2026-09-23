# Support the Cubis OPC UA balance (non-LADS instruments)

## What the screenshot shows

The Cubis balance is a plain OPC UA server, not a LADS server:

```text
Objects
 ├─ DeviceSet ──────── DeviceFeatures only (no device below it)
 └─ Simple Scale ───── CurrentWeight, RegisteredWeight, DeviceID,
                       Manufacturer, Model, HardwareRevision,
                       ClearTare / RegisterWeight (methods)
```

There is no FunctionalUnitSet, no ProgramManager and no ResultSet, so the
connector's LADS walk fails with `child "FunctionalUnitSet" not found` and no
measurement is ever collected. Today the connector only knows how to read a
LADS result archive.

## What will be added

A second reading mode for instruments that expose their measurements as plain
OPC UA variables instead of a LADS result archive.

1. **Two instrument modes.** Every OPC UA instrument gets a reading mode:
   *LADS result archive* (today's behaviour, unchanged) or *single values*.
   New instruments default to automatic: the connector tries LADS first, and
   when the instrument has no LADS model it says so and offers the value mode
   instead of failing in a loop.

2. **Value watching.** In value mode the connector watches one chosen trigger
   variable (on the Cubis: `RegisteredWeight`, the value the operator confirms
   on the balance). Every new value with a new timestamp becomes one
   measurement: the trigger value plus any other ticked variables are read and
   uploaded to LabNote as a single-value result with its unit (`g`).
   `CurrentWeight` stays available but is off by default, because a live weight
   changes continuously and would flood the queue.

3. **Value detection in setup.** "Detect values" is extended to browse plain
   OPC UA objects (`Simple Scale` and anything under `DeviceSet`), listing
   every readable numeric variable with its engineering unit, so the operator
   ticks the ones LabNote should receive and picks the trigger.

4. **A Cubis profile.** A new built-in profile pre-fills the trigger and value
   names for Sartorius Cubis OPC UA balances, plus manufacturer/model/serial
   reading, so a fresh install needs no manual node ids.

5. **Clearer error text.** Instead of `browse LADS model: child
   "FunctionalUnitSet" not found`, the instrument row will read: this
   instrument is not a LADS device — choose the values to read in setup.

## Technical notes

- `model.Instrument` gains `OPCUAMode` (`auto` | `lads` | `values`),
  `TriggerNodeID` + its namespace URI, and reuses `Parameters` (with node ids)
  for the variables to read.
- `internal/lads`: new `BrowseVariables(ctx, rootNodeID)` returning readable
  numeric variables with `EngineeringUnits` / `EURange`, and `PlainDevices`
  that finds candidate objects under `Objects` when `DeviceSet` holds no LADS
  device.
- `internal/device/supervisor.go`: `session` branches after `resolveDevice` —
  when `ResultSetNodes` fails or mode is `values`, run a `valueSession` that
  monitors the trigger node (plus a 5 s poll fallback) and enqueues one
  `model.Result` per new source timestamp; `ExternalResultID` =
  `<external_device_id>:<RFC3339 nano source timestamp>` so restarts dedupe
  through the existing 409 path.
- Result shape stays exactly as LabNote expects: `summary` carries the named
  values, `unit_y` the trigger unit, `points` empty, `measured_at` the OPC UA
  source timestamp.
- `internal/profiles/builtin/sartorius-cubis-opcua.yaml` for the Cubis names.
- Setup UI (`internal/webui/static/index.html` + `app.js`) gets the mode
  selector, trigger picker and detected-value list; the React preview under
  `src/` mirrors it.
- Tests: a fake plain OPC UA server in the acceptance suite (weight registered
  twice → two results, no duplicate), plus unit tests for value mapping and
  trigger dedupe.
