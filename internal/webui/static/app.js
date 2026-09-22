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

// busy disables a button while its action runs, so the same request cannot be
// started twice and the user can see that something is happening.
async function busy(btn, label, fn) {
  const original = btn.textContent;
  btn.disabled = true;
  btn.classList.add("working");
  btn.textContent = label;
  try {
    return await fn();
  } finally {
    btn.disabled = false;
    btn.classList.remove("working");
    btn.textContent = original;
  }
}

// --- field level validation ---------------------------------------------

function setError(id, message) {
  const field = $(id);
  const box = $(id + "Error");
  if (box) {
    box.textContent = message || "";
    box.hidden = !message;
  }
  field.classList.toggle("invalid", !!message);
  field.setAttribute("aria-invalid", message ? "true" : "false");
  return !message;
}

function validateSetup() {
  const url = $("labnoteUrl").value.trim();
  let ok = setError("labnoteUrl", !url
    ? "Enter the address of your LabNote instance."
    : /^https:\/\/.+/i.test(url)
      ? ""
      : "The address must start with https://");
  const key = $("apiKey").value.trim();
  const stored = latest && latest.api_key_stored;
  ok = setError("apiKey", key || stored ? "" : "Paste the ingest API key from LabNote.") && ok;
  return ok;
}

function validateInstrument() {
  const push = $("insKind").value === "push";
  let ok = setError("insName", $("insName").value.trim() ? "" : "Give the instrument a name people recognise.");
  const id = $("insExternal").value.trim();
  ok = setError("insExternal", !id
    ? "Enter the device ID used in LabNote."
    : /^[A-Za-z0-9._-]+$/.test(id)
      ? ""
      : "Use letters, numbers, dot, dash or underscore only.") && ok;
  if (!push) {
    const ep = $("insEndpoint").value.trim();
    ok = setError("insEndpoint", !ep
      ? "Enter the instrument address, e.g. opc.tcp://192.168.1.50:4840"
      : /^opc\.tcp:\/\/[^\s/]+/i.test(ep)
        ? ""
        : "The address must look like opc.tcp://host:port") && ok;
  } else {
    setError("insEndpoint", "");
  }
  return ok;
}

// Clear an error as soon as the field is corrected.
["labnoteUrl", "apiKey"].forEach((id) =>
  $(id).addEventListener("input", () => setError(id, "")),
);
["insName", "insExternal", "insEndpoint"].forEach((id) =>
  $(id).addEventListener("input", () => setError(id, "")),
);

function instrumentForm() {
  return {
    id: $("insId").value,
    kind: $("insKind").value,
    parameters: detectedParams.filter((p) => p.path),
    name: $("insName").value.trim(),
    external_device_id: $("insExternal").value.trim(),
    opcua_endpoint_url: $("insEndpoint").value.trim(),
    opcua_username: $("insUser").value,
    opcua_password: $("insPass").value,
    vendor: $("insVendor").value,
    model: $("insModel").value,
    device_type: $("insType").value,
    lads_node_id: $("insNode").value.trim(),
    // Remembered so the device node can be found again after the instrument
    // renumbers its address space.
    lads_namespace_uri: detectedNamespace,
    profile: $("insProfile").value,
    default_unit_x: $("insUnitX").value,
    default_unit_y: $("insUnitY").value,
    security_mode: "SignAndEncrypt",
    // "auto" uses the strongest encryption the instrument offers instead of
    // insisting on one policy that some instruments do not implement.
    security_policy: "auto",
    allow_sign_only: $("insAllowSign").checked,
  };
}

// --- copy buttons --------------------------------------------------------

document.querySelectorAll("button.copy").forEach((btn) => {
  btn.addEventListener("click", async () => {
    const text = $(btn.dataset.copy).textContent.trim();
    try {
      await navigator.clipboard.writeText(text);
      const was = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(() => (btn.textContent = was), 1500);
    } catch {
      // Clipboard access can be refused; select the text so it can be copied.
      const range = document.createRange();
      range.selectNodeContents($(btn.dataset.copy));
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
    }
  });
});

// --- discovered instruments ---------------------------------------------

let discoverAbort = null;

function renderDiscovered(servers) {
  const box = $("discoverResults");
  box.innerHTML = "";
  if (!servers.length) {
    hint($("discoverHint"), "No OPC UA instruments answered on this network. Enter the address by hand.", "bad");
    return;
  }
  const table = document.createElement("table");
  table.innerHTML = "<thead><tr><th>Instrument</th><th>Address</th><th></th></tr></thead><tbody></tbody>";
  const body = table.querySelector("tbody");
  servers.forEach((srv) => {
    const row = document.createElement("tr");
    const name = document.createElement("td");
    name.dataset.label = "Instrument";
    name.textContent = srv.server_name || "OPC UA server";
    const addr = document.createElement("td");
    addr.dataset.label = "Address";
    addr.textContent = srv.endpoint_url + (srv.note ? " — " + srv.note : "");
    const act = document.createElement("td");
    if (srv.already_added) {
      act.textContent = "already added";
    } else {
      act.appendChild(button("Use this", "secondary", () => {
        $("insKind").value = "opcua";
        showPush(null);
        $("insEndpoint").value = srv.endpoint_url;
        setError("insEndpoint", "");
        if (!$("insName").value) $("insName").value = srv.server_name || "";
        $("insAllowSign").checked = !srv.secure && !!srv.sign_only;
        const logins = srv.logins || [];
        if (logins.includes("user name") && !logins.includes("certificate")) {
          hint($("instrumentHint"), "Address filled in. This instrument asks for a user name and password — enter them, then Test connection.", "ok");
        } else if (!srv.secure && srv.sign_only) {
          hint($("instrumentHint"), "Address filled in. This instrument only offers a signed, unencrypted connection, so that option has been ticked for you.", "ok");
        } else {
          hint($("instrumentHint"), "Address filled in. Give it a device ID, then Test connection.", "ok");
        }
        $("insExternal").focus();
      }));
    }
    row.append(name, addr, act);
    body.appendChild(row);
  });
  box.appendChild(table);
  hint($("discoverHint"), `Found ${servers.length} instrument(s).`, "ok");
}

// --- parameters ----------------------------------------------------------

let detectedParams = [];
// Namespace the detected device node belongs to; saved with the instrument.
let detectedNamespace = "";

function renderParams(params) {
  detectedParams = params || [];
  $("paramBox").hidden = detectedParams.length === 0;
  const list = $("paramList");
  list.innerHTML = "";
  detectedParams.forEach((p, i) => {
    const label = document.createElement("label");
    label.className = "inline";
    const box = document.createElement("input");
    box.type = "checkbox";
    box.checked = !!p.enabled;
    box.disabled = p.kind === "expected";
    box.addEventListener("change", () => { detectedParams[i].enabled = box.checked; });
    const text = document.createElement("span");
    let suffix = p.unit ? ` (${p.unit})` : "";
    if (p.kind === "series") suffix += " — curve";
    if (p.kind === "expected") suffix += " — appears after the first measurement";
    text.textContent = ` ${p.name}${suffix}`;
    label.append(box, text);
    list.appendChild(label);
  });
}

function setAllParams(on) {
  detectedParams.forEach((p) => {
    if (p.kind !== "expected") p.enabled = on;
  });
  renderParams(detectedParams);
}

$("paramAll").addEventListener("click", () => setAllParams(true));
$("paramNone").addEventListener("click", () => setAllParams(false));

// --- device picker (servers that host more than one LADS device) ----------

function renderDevicePicker(devices) {
  const row = $("rowDevicePick");
  const select = $("insDevice");
  if (!devices || devices.length < 2) {
    row.hidden = true;
    return;
  }
  select.innerHTML = "";
  devices.forEach((d) => {
    const opt = document.createElement("option");
    opt.value = d.node_id;
    opt.dataset.namespace = d.namespace_uri || "";
    opt.textContent = `${d.name || "device"}${d.model ? " — " + d.model : ""}`;
    select.append(opt);
  });
  const current = $("insNode").value;
  if (devices.some((d) => d.node_id === current)) select.value = current;
  else {
    $("insNode").value = devices[0].node_id;
    detectedNamespace = devices[0].namespace_uri || detectedNamespace;
  }
  row.hidden = false;
}

$("insDevice").addEventListener("change", () => {
  const opt = $("insDevice").selectedOptions[0];
  if (!opt) return;
  $("insNode").value = opt.value;
  detectedNamespace = opt.dataset.namespace || detectedNamespace;
  hint($("instrumentHint"), `This instrument will report “${opt.textContent}”.`, "ok");
});

function fillForm(ins) {
  $("insId").value = ins.id || "";
  $("insKind").value = ins.kind || "opcua";
  showPush(ins);
  renderParams(ins.parameters || []);
  renderDevicePicker(null);
  $("insName").value = ins.name || "";
  $("insExternal").value = ins.external_device_id || "";
  $("insEndpoint").value = ins.opcua_endpoint_url || "";
  $("insUser").value = ins.opcua_username || "";
  $("insPass").value = "";
  $("insPass").placeholder = ins.password_stored
    ? "stored — leave blank to keep the current password"
    : "stored in the operating system credential store";
  $("insVendor").value = ins.vendor || "";
  $("insModel").value = ins.model || "";
  $("insType").value = ins.device_type || "";
  $("insNode").value = ins.lads_node_id || "";
  detectedNamespace = ins.lads_namespace_uri || "";
  $("insAllowSign").checked = !!ins.allow_sign_only;
  $("insProfile").value = ins.profile || "generic-lads";
  $("insUnitX").value = ins.default_unit_x || "";
  $("insUnitY").value = ins.default_unit_y || "";
  ["labnoteUrl", "apiKey", "insName", "insExternal", "insEndpoint"].forEach((id) => setError(id, ""));
  $("cancelEdit").hidden = !ins.id;
  $("saveInstrument").textContent = ins.id ? "Save changes" : "Save instrument";
  if (ins.id) $("instrumentCard").scrollIntoView({ behavior: "smooth", block: "start" });
}

$("cancelEdit").addEventListener("click", () => {
  fillForm({});
  hint($("instrumentHint"), "Editing cancelled.");
});

// showPush reveals the push address for instruments that send their reports.
function showPush(ins) {
  const push = $("insKind").value === "push";
  $("pushBox").hidden = !push;
  // Fields and buttons that only apply to instruments the connector reads.
  for (const id of ["rowEndpoint", "rowNode", "rowProfile", "rowSign", "rowUser", "rowPass"]) {
    $(id).hidden = push;
  }
  $("opcuaActions").hidden = push;
  $("detectParams").hidden = push;
  $("paramBox").hidden = push || $("paramBox").hidden;
  if (push) $("rowDevicePick").hidden = true;
  $("securityHint").hidden = push;
  $("testInstrument").textContent = push ? "Check for a report" : "Test connection";
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
      <td data-label="Name">${escape(ins.name)}</td>
      <td data-label="Device ID">${escape(ins.external_device_id)}</td>
      <td data-label="Endpoint">${escape(ins.kind === "push" ? "pushes reports to this connector" : ins.opcua_endpoint_url)}</td>
      <td data-label="Status"><span class="pill ${status}">${status}</span></td>
      <td data-label="Last result">${live.last_result_at ? new Date(live.last_result_at).toLocaleString() : "—"}</td>
      <td data-label=""></td>`;

    // Plain-language notes about the live connection: which encryption is in
    // use, when the instrument certificate expires, and any non-fatal warning.
    const notes = [];
    if (live.negotiated_policy) {
      notes.push(
        `Connection: ${live.negotiated_mode === "Sign" ? "signed, not encrypted" : "encrypted"} (${live.negotiated_policy})`,
      );
    }
    if (live.server_cert_not_after) {
      const until = new Date(live.server_cert_not_after);
      const days = Math.round((until - Date.now()) / 86400000);
      notes.push(
        days <= 30
          ? `Instrument certificate expires in ${days} day(s) — ${until.toLocaleDateString()}`
          : `Instrument certificate valid until ${until.toLocaleDateString()}`,
      );
    }
    if (live.warning) notes.push(live.warning);
    if (notes.length) {
      const note = document.createElement("div");
      note.className = "note";
      note.textContent = notes.join(" · ");
      tr.children[3].append(note);
    }

    const cell = tr.lastElementChild;
    const edit = button("Edit", "secondary", () => fillForm(ins));
    cell.append(edit);

    if (live.pending_trust && live.server_cert_sha256) {
      const trust = button("Trust certificate", "secondary", (ev) =>
        confirmAction(ev.currentTarget, `Trust ${live.server_cert_sha256.slice(0, 16)}…?`, async () => {
          await api(`/api/instruments/${ins.id}/trust`, {
            method: "POST",
            body: JSON.stringify({ fingerprint: live.server_cert_sha256 }),
          });
          refresh();
        }),
      );
      cell.append(trust);
    }

    cell.append(
      button("Remove", "link", (ev) =>
        confirmAction(ev.currentTarget, "Really remove?", async () => {
          await api(`/api/instruments/${ins.id}`, { method: "DELETE" });
          refresh();
        }),
      ),
    );
    tbody.append(tr);
  });

  if (!(s.instruments || []).length) {
    tbody.innerHTML = `<tr><td colspan="6">No instruments configured yet.</td></tr>`;
  }
}

// confirmAction replaces the browser's own dialog: the button asks once more in
// place, and reverts if it is not confirmed within a few seconds.
function confirmAction(btn, question, run) {
  if (btn.dataset.confirming === "1") {
    btn.dataset.confirming = "0";
    run();
    return;
  }
  const original = btn.textContent;
  btn.dataset.confirming = "1";
  btn.textContent = question;
  btn.classList.add("confirming");
  setTimeout(() => {
    if (btn.dataset.confirming !== "1") return;
    btn.dataset.confirming = "0";
    btn.textContent = original;
    btn.classList.remove("confirming");
  }, 5000);
}

function button(label, cls, onClick) {
  const b = document.createElement("button");
  b.type = "button";
  b.textContent = label;
  b.className = cls;
  b.addEventListener("click", onClick);
  return b;
}

function escape(v) {
  return String(v ?? "").replace(/[<>&"]/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" }[c]));
}

// setStep marks a setup step as done or still open, so it is obvious at a
// glance what is left to do.
function setStep(navId, stateId, done, text) {
  $(navId).classList.toggle("done", done);
  const badge = $(stateId);
  if (badge) {
    badge.textContent = text;
    badge.className = "state" + (done ? " done" : "");
  }
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

  const connected = !!(s.labnote_url && s.api_key_stored);
  setStep("navStep1", "state1", connected, connected ? "connected" : "not set up");
  setStep("navStep2", null, !!s.client_certificate_fingerprint);
  const count = (s.instruments || []).length;
  setStep("navStep3", "state3", count > 0, count ? `${count} configured` : "none yet");
  setStep("navStep4", null, s.runtime.status === "online");

  renderInstruments(s);
}

async function refresh() {
  try {
    render(await api("/api/state"));
  } catch (err) {
    hint($("setupHint"), err.message, "bad");
  }
}

$("saveSetup").addEventListener("click", async (ev) => {
  if (!validateSetup()) {
    hint($("setupHint"), "Please correct the highlighted fields.", "bad");
    return;
  }
  hint($("setupHint"), "Testing the connection to LabNote…");
  await busy(ev.currentTarget, "Testing…", async () => {
    try {
      await api("/api/setup", {
        method: "POST",
        body: JSON.stringify({
          labnote_url: $("labnoteUrl").value.trim(),
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
});

$("saveInstrument").addEventListener("click", async (ev) => {
  if (!validateInstrument()) {
    hint($("instrumentHint"), "Please correct the highlighted fields.", "bad");
    return;
  }
  await busy(ev.currentTarget, "Saving…", async () => {
    try {
      await api("/api/instruments", { method: "POST", body: JSON.stringify(instrumentForm()) });
      $("insPass").value = "";
      hint($("instrumentHint"), "Saved. The connector is establishing the session.", "ok");
      fillForm({});
      refresh();
    } catch (err) {
      hint($("instrumentHint"), err.message, "bad");
    }
  });
});

$("testInstrument").addEventListener("click", async (ev) => {
  if (!validateInstrument()) {
    hint($("instrumentHint"), "Please correct the highlighted fields.", "bad");
    return;
  }
  hint($("instrumentHint"), "Contacting the instrument…");
  await busy(ev.currentTarget, "Testing…", async () => {
    try {
      const report = await api("/api/instruments/test", { method: "POST", body: JSON.stringify(instrumentForm()) });
      renderDevicePicker(report.devices);
      const devices = (report.devices || []).map((d) => `${d.name} (${d.model || "unknown model"})`).join(", ");
      let message = report.message + (devices ? " — " + devices : "");
      if ((report.devices || []).length > 1) {
        message += " Choose which one this instrument entry should report.";
      }
      if (report.trust_required) {
        message += " Save it, then use “Trust certificate” in the table above.";
      }
      hint($("instrumentHint"), message, report.ok ? "ok" : "bad");
      refresh();
    } catch (err) {
      hint($("instrumentHint"), err.message, "bad");
    }
  });
});

$("discover").addEventListener("click", async (ev) => {
  hint($("discoverHint"), "Searching the network… this takes up to a minute.");
  discoverAbort = new AbortController();
  $("discoverCancel").hidden = false;
  await busy(ev.currentTarget, "Searching…", async () => {
    try {
      const extra = $("discoverExtra").value.trim();
      const res = await api("/api/discover", {
        method: "POST",
        signal: discoverAbort.signal,
        body: JSON.stringify({ extra: extra ? [extra] : [] }),
      });
      renderDiscovered(res.servers || []);
    } catch (err) {
      if (err.name === "AbortError") hint($("discoverHint"), "Search stopped.");
      else hint($("discoverHint"), err.message, "bad");
    } finally {
      $("discoverCancel").hidden = true;
      discoverAbort = null;
    }
  });
});

$("discoverCancel").addEventListener("click", () => {
  if (discoverAbort) discoverAbort.abort();
});

$("detectParams").addEventListener("click", async (ev) => {
  if (!validateInstrument()) {
    hint($("instrumentHint"), "Please correct the highlighted fields.", "bad");
    return;
  }
  hint($("instrumentHint"), "Asking the instrument what it measures…");
  await busy(ev.currentTarget, "Detecting…", async () => {
    try {
      const report = await api("/api/instruments/parameters", {
        method: "POST",
        body: JSON.stringify(instrumentForm()),
      });
      renderParams((report.parameters || []).map((p) => ({ ...p, enabled: !!p.recommended })));
      if (report.lads_node_id && !$("insNode").value) $("insNode").value = report.lads_node_id;
      if (report.lads_namespace_uri) detectedNamespace = report.lads_namespace_uri;
      hint($("instrumentHint"), report.message, report.ok ? "ok" : "bad");
    } catch (err) {
      hint($("instrumentHint"), err.message, "bad");
    }
  });
});

$("insKind").addEventListener("change", () => showPush(null));

$("autoUpdate").addEventListener("change", async (e) => {
  await api("/api/settings", { method: "POST", body: JSON.stringify({ auto_update: e.target.checked }) });
});

refresh();
setInterval(refresh, 5000);
