// Local setup UI. Talks only to the connector running on this machine.
const $ = (id) => document.getElementById(id);
let latest = null;

async function api(path, options) {
  const res = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options?.headers || {}) },
  });
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(body.error || `request failed (${res.status})`);
  return body;
}

function hint(el, message, kind) {
  el.textContent = message || "";
  el.className = "hint" + (kind ? " " + kind : "");
}

function instrumentForm() {
  return {
    id: $("insId").value,
    kind: $("insKind").value,
    name: $("insName").value,
    external_device_id: $("insExternal").value,
    opcua_endpoint_url: $("insEndpoint").value,
    vendor: $("insVendor").value,
    model: $("insModel").value,
    device_type: $("insType").value,
    lads_node_id: $("insNode").value,
    profile: $("insProfile").value,
    default_unit_x: $("insUnitX").value,
    default_unit_y: $("insUnitY").value,
    security_mode: "SignAndEncrypt",
    security_policy: "Basic256Sha256",
  };
}

function fillForm(ins) {
  $("insId").value = ins.id || "";
  $("insKind").value = ins.kind || "opcua";
  showPush(ins);
  $("insName").value = ins.name || "";
  $("insExternal").value = ins.external_device_id || "";
  $("insEndpoint").value = ins.opcua_endpoint_url || "";
  $("insVendor").value = ins.vendor || "";
  $("insModel").value = ins.model || "";
  $("insType").value = ins.device_type || "";
  $("insNode").value = ins.lads_node_id || "";
  $("insProfile").value = ins.profile || "generic-lads";
  $("insUnitX").value = ins.default_unit_x || "";
  $("insUnitY").value = ins.default_unit_y || "";
}

// showPush reveals the push address for instruments that send their reports.
function showPush(ins) {
  const push = $("insKind").value === "push";
  $("pushBox").hidden = !push;
  if (!push || !latest) return;
  const i = latest.ingest || {};
  $("pushFingerprint").textContent = i.certificate_fingerprint || "generated when the first push instrument is saved";
  $("pushUrl").textContent = ins && ins.ingest_token
    ? `${i.scheme}://${i.host}:${i.port}/ingest/${ins.ingest_token}`
    : "save the instrument to generate its address";
}

function renderInstruments(s) {
  const byId = {};
  (s.runtime.devices || []).forEach((d) => (byId[d.external_device_id] = d));
  const tbody = $("instrumentTable").querySelector("tbody");
  tbody.innerHTML = "";

  (s.instruments || []).forEach((ins) => {
    const live = byId[ins.external_device_id] || {};
    const tr = document.createElement("tr");
    const status = live.connection_status || "unknown";
    tr.innerHTML = `
      <td>${escape(ins.name)}</td>
      <td>${escape(ins.external_device_id)}</td>
      <td>${escape(ins.kind === "push" ? "pushes reports to this connector" : ins.opcua_endpoint_url)}</td>
      <td><span class="pill ${status}">${status}</span></td>
      <td>${live.last_result_at ? new Date(live.last_result_at).toLocaleString() : "—"}</td>
      <td></td>`;

    const cell = tr.lastElementChild;
    const edit = button("Edit", "secondary", () => fillForm(ins));
    cell.append(edit);

    if (live.pending_trust && live.server_cert_sha256) {
      const trust = button("Trust certificate", "secondary", async () => {
        if (!confirm(`Trust this instrument certificate?\n\n${live.server_cert_sha256}`)) return;
        await api(`/api/instruments/${ins.id}/trust`, {
          method: "POST",
          body: JSON.stringify({ fingerprint: live.server_cert_sha256 }),
        });
        refresh();
      });
      cell.append(trust);
    }

    cell.append(
      button("Remove", "link", async () => {
        if (!confirm(`Remove ${ins.name || ins.external_device_id}?`)) return;
        await api(`/api/instruments/${ins.id}`, { method: "DELETE" });
        refresh();
      }),
    );
    tbody.append(tr);
  });

  if (!(s.instruments || []).length) {
    tbody.innerHTML = `<tr><td colspan="6">No instruments configured yet.</td></tr>`;
  }
}

function button(label, cls, onClick) {
  const b = document.createElement("button");
  b.textContent = label;
  b.className = cls;
  b.style.marginRight = ".5rem";
  b.addEventListener("click", onClick);
  return b;
}

function escape(v) {
  return String(v ?? "").replace(/[<>&"]/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" }[c]));
}

function render(s) {
  latest = s;
  $("version").textContent = s.version;
  $("statusDot").className = "dot " + s.runtime.status;
  $("connectorStatus").textContent = s.runtime.status;
  $("queueDepth").textContent = `${s.runtime.queue_depth} result(s)`;
  $("lastError").textContent = s.runtime.last_error || "none";
  $("fingerprint").textContent = s.client_certificate_fingerprint || "not generated yet";
  $("autoUpdate").checked = !!s.auto_update;
  $("updateStatus").textContent = s.update.update_available
    ? `${s.version} — update ${s.update.latest_version} available`
    : `${s.version} — up to date`;

  if (document.activeElement !== $("labnoteUrl")) $("labnoteUrl").value = s.labnote_url || "";
  if (document.activeElement !== $("name")) $("name").value = s.name || "";
  if (document.activeElement !== $("location")) $("location").value = s.location || "";
  $("apiKey").placeholder = s.api_key_stored
    ? "stored — leave blank to keep the current key"
    : "stored in the operating system credential store";

  const select = $("insProfile");
  if (select.options.length !== (s.profiles || []).length) {
    select.innerHTML = "";
    (s.profiles || []).forEach((p) => {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = p.description || p.id;
      select.append(opt);
    });
  }

  renderInstruments(s);
}

async function refresh() {
  try {
    render(await api("/api/state"));
  } catch (err) {
    hint($("setupHint"), err.message, "bad");
  }
}

$("saveSetup").addEventListener("click", async () => {
  hint($("setupHint"), "Testing…");
  try {
    await api("/api/setup", {
      method: "POST",
      body: JSON.stringify({
        labnote_url: $("labnoteUrl").value,
        api_key: $("apiKey").value,
        name: $("name").value,
        location: $("location").value,
      }),
    });
    $("apiKey").value = "";
    hint($("setupHint"), "Connected to LabNote and saved.", "ok");
    refresh();
  } catch (err) {
    hint($("setupHint"), err.message, "bad");
  }
});

$("saveInstrument").addEventListener("click", async () => {
  hint($("instrumentHint"), "Saving…");
  try {
    await api("/api/instruments", { method: "POST", body: JSON.stringify(instrumentForm()) });
    hint($("instrumentHint"), "Saved. The connector is establishing the session.", "ok");
    fillForm({});
    refresh();
  } catch (err) {
    hint($("instrumentHint"), err.message, "bad");
  }
});

$("testInstrument").addEventListener("click", async () => {
  hint($("instrumentHint"), "Browsing the instrument…");
  try {
    const report = await api("/api/instruments/test", { method: "POST", body: JSON.stringify(instrumentForm()) });
    const devices = (report.devices || []).map((d) => `${d.name} (${d.model || "unknown model"})`).join(", ");
    hint($("instrumentHint"), report.message + (devices ? " — " + devices : ""), report.ok ? "ok" : "bad");
    refresh();
  } catch (err) {
    hint($("instrumentHint"), err.message, "bad");
  }
});

$("insKind").addEventListener("change", () => showPush(null));

$("autoUpdate").addEventListener("change", async (e) => {
  await api("/api/settings", { method: "POST", body: JSON.stringify({ auto_update: e.target.checked }) });
});

refresh();
setInterval(refresh, 5000);
