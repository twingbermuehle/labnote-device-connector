// Sample data for the setup-screen preview. The shipped binary serves its own
// embedded screens and reads the real values from the running connector.

export type DeviceStatus = "connected" | "disconnected" | "error" | "unknown";

export const connector = {
  name: "lab-connector-01",
  location: "Building C, Lab 2.14",
  version: "v1.0.0",
  status: "degraded" as "online" | "degraded" | "offline",
  updateNote: "up to date",
  labnoteUrl: "https://labnote-light.com",
  clientFingerprint:
    "9F:2C:41:AA:07:BE:53:12:D8:64:19:7C:E0:35:8B:F1:24:6D:90:AB:CD:11:52:03:7E:48:C9:60:1F:22:BB:84",
  lastError: "HPLC-09: instrument certificate is not trusted yet — confirm the fingerprint below.",
};

export const queue = { depth: 3 };

export const devices: {
  name: string;
  externalDeviceId: string;
  endpoint: string;
  status: DeviceStatus;
  lastResult: string;
  method: string;
  message?: string;
}[] = [
  {
    name: "HPLC 07",
    externalDeviceId: "HPLC-07",
    endpoint: "opc.tcp://hplc-07.lab.example.com:4840",
    status: "connected",
    lastResult: "14:52:07",
    method: "Gradient 12 min",
  },
  {
    name: "Plate reader 02",
    externalDeviceId: "PLATE-02",
    endpoint: "opc.tcp://192.168.1.62:4840",
    status: "connected",
    lastResult: "14:38:11",
    method: "Absorbance 405 nm",
  },
  {
    name: "HPLC 09",
    externalDeviceId: "HPLC-09",
    endpoint: "opc.tcp://192.168.1.50:4840",
    status: "error",
    lastResult: "—",
    method: "—",
    message:
      "Instrument certificate is not trusted yet (fingerprint 3B:71:...:C4). Confirm it to allow the connection.",
  },
];

export const profileOptions = [
  { id: "generic-lads", label: "Base LADS companion specification (SPECTARIS reference server)" },
];

/** Instruments found on the network by the connector's search. */
export const discovered: { name: string; endpoint: string; added: boolean }[] = [
  { name: "LADS LuminescenceReader", endpoint: "opc.tcp://192.168.1.42:4840", added: false },
  { name: "Agilent 1260 Infinity II", endpoint: "opc.tcp://192.168.1.50:4840", added: true },
  { name: "Mettler Toledo XPR", endpoint: "opc.tcp://192.168.1.61:4840", added: false },
];

/** Measurable parameters detected on the selected instrument. */
export const parameters: { name: string; unit?: string; kind: string; enabled: boolean }[] = [
  { name: "Luminescence", unit: "RLU", kind: "series", enabled: true },
  { name: "Temperature", unit: "°C", kind: "value", enabled: true },
  { name: "ShakerController", kind: "expected", enabled: false },
  { name: "WastePump", kind: "expected", enabled: false },
];
