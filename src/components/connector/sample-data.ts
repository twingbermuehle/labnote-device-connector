// Sample data for the setup-screen preview. The shipped binary serves its own
// embedded screens and reads the real values from the running connector.

export type DeviceStatus = "connected" | "disconnected" | "error" | "unknown";

export const connector = {
  name: "lab-connector-01",
  location: "Building C, Lab 2.14",
  version: "v1.6.0",
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
  /** How the live connection is protected, as reported by the connector. */
  security?: string;
  /** When the instrument's own certificate expires. */
  certificateUntil?: string;
  message?: string;
}[] = [
  {
    name: "HPLC 07",
    externalDeviceId: "HPLC-07",
    endpoint: "opc.tcp://hplc-07.lab.example.com:4840",
    status: "connected",
    lastResult: "14:52:07",
    method: "Gradient 12 min",
    security: "encrypted (Basic256Sha256)",
    certificateUntil: "14 March 2027",
  },
  {
    name: "Plate reader 02",
    externalDeviceId: "PLATE-02",
    endpoint: "opc.tcp://192.168.1.62:4840",
    status: "connected",
    lastResult: "14:38:11",
    method: "Absorbance 405 nm",
    security: "encrypted (Aes256Sha256RsaPss)",
    certificateUntil: "2 December 2026",
  },
  {
    name: "Balance 03 (sends its reports)",
    externalDeviceId: "BAL-03",
    endpoint: "pushes reports to this connector",
    status: "connected",
    lastResult: "14:31:55",
    method: "Weighing report",
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
  {
    id: "sartorius-cubis-opcua",
    label: "Sartorius Cubis / Simple Scale (plain OPC UA, no LADS model)",
  },
];

/** How an instrument is read, shown in the setup preview. */
export const readingModes = [
  { id: "auto", label: "Automatic — LADS results, otherwise single values" },
  { id: "lads", label: "LADS results only" },
  { id: "values", label: "Single values (balances and other simple instruments)" },
];

/** Instruments found on the network by the connector's search. */
export const discovered: {
  name: string;
  endpoint: string;
  added: boolean;
  /** Plain-language explanation of what this instrument offers. */
  note?: string;
}[] = [
  {
    name: "LADS LuminescenceReader",
    endpoint: "opc.tcp://192.168.1.42:4840",
    added: false,
    note: "encrypted, certificate login",
  },
  { name: "Agilent 1260 Infinity II", endpoint: "opc.tcp://192.168.1.50:4840", added: true },
  {
    name: "Mettler Toledo XPR",
    endpoint: "opc.tcp://192.168.1.61:4840",
    added: false,
    note: "encrypted, asks for a user name and password",
  },
];

/** LADS devices found on one instrument, offered for selection. */
export const deviceChoices = [
  { nodeId: "ns=2;i=5001", label: "LuminescenceReader — LumiMax 400" },
  { nodeId: "ns=2;i=7001", label: "Shaker — LumiShake 10" },
];

/** Measurable parameters detected on the selected instrument. */
export const parameters: { name: string; unit?: string; kind: string; enabled: boolean }[] = [
  { name: "Luminescence", unit: "RLU", kind: "series", enabled: true },
  { name: "Temperature", unit: "°C", kind: "value", enabled: true },
  { name: "ShakerController", kind: "expected", enabled: false },
  { name: "WastePump", kind: "expected", enabled: false },
];
