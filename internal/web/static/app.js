const state = {
  token: sessionStorage.getItem("goxidized_token") || "",
  principal: null,
  devices: [],
  jobs: [],
  drivers: [],
  ready: null,
  filters: { group: "", vendor: "", site: "", status: "", search: "" },
  selectedId: "",
  selectedDriver: "",
  tab: "overview",
  revisions: new Map(),
  latestConfig: new Map(),
  diffs: new Map(),
  jobDetails: new Map(),
};

const app = document.getElementById("app");
const login = document.getElementById("login");
const toast = document.getElementById("toast");

document.addEventListener("DOMContentLoaded", () => {
  bindEvents();
  boot();
});

function bindEvents() {
  document.getElementById("token-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = document.getElementById("token-input");
    state.token = input.value.trim();
    sessionStorage.setItem("goxidized_token", state.token);
    await boot();
  });

  document.getElementById("replace-token").addEventListener("click", () => {
    showLogin("");
  });
  document.getElementById("logout").addEventListener("click", logout);
  document.getElementById("refresh").addEventListener("click", () => loadDashboard());
  document.getElementById("trigger-selected").addEventListener("click", () => {
    const device = selectedDevice();
    if (device) {
      triggerDeviceBackup(device.id);
    }
  });
  document.getElementById("reload-inventory").addEventListener("click", reloadInventory);
  document.getElementById("driver-test").addEventListener("click", testSelectedDriver);

  for (const id of ["group-filter", "vendor-filter", "site-filter", "status-filter"]) {
    document.getElementById(id).addEventListener("change", (event) => {
      state.filters[id.replace("-filter", "")] = event.target.value;
      renderDevices();
    });
  }
  document.getElementById("search").addEventListener("input", (event) => {
    state.filters.search = event.target.value.trim().toLowerCase();
    renderDevices();
  });

  document.querySelectorAll(".tab").forEach((tab) => {
    tab.addEventListener("click", () => {
      if (tab.disabled) return;
      state.tab = tab.dataset.tab;
      renderDetail();
    });
  });
}

async function boot() {
  try {
    state.principal = await api("/api/v1/auth/me");
    showApp();
    await loadDashboard({ silent: true });
  } catch (error) {
    sessionStorage.removeItem("goxidized_token");
    state.token = "";
    showLogin(error.status === 401 ? "" : "Authentication failed.");
  }
}

function showLogin(message) {
  app.hidden = true;
  login.hidden = false;
  document.getElementById("login-error").textContent = message || "";
}

function showApp() {
  login.hidden = true;
  app.hidden = false;
  renderPrincipal();
  syncRBAC();
}

async function loadDashboard(options = {}) {
  if (!state.principal) return;
  try {
    const [ready, devices, jobs, drivers] = await Promise.all([
      getOptional("/readyz", null),
      api("/api/v1/devices"),
      hasPermission("jobs:read") ? getOptional("/api/v1/jobs?limit=1000", []) : Promise.resolve([]),
      hasPermission("drivers:read") ? getOptional("/api/v1/drivers", { drivers: [] }) : Promise.resolve({ drivers: [] }),
    ]);
    state.ready = ready;
    state.devices = Array.isArray(devices) ? devices : [];
    state.jobs = Array.isArray(jobs) ? jobs : [];
    state.drivers = Array.isArray(drivers.drivers) ? drivers.drivers : [];
    if (!state.selectedId && state.devices.length > 0) {
      state.selectedId = state.devices[0].id;
    }
    if (!state.selectedDriver && state.drivers.length > 0) {
      state.selectedDriver = state.drivers[0];
    }
    renderFilters();
    renderDevices();
    renderSummary();
    renderDrivers();
    renderDetail();
    if (!options.silent) {
      showToast("Dashboard refreshed.");
    }
  } catch (error) {
    handleAPIError(error);
  }
}

async function api(path, options = {}) {
  const init = {
    method: options.method || "GET",
    credentials: "include",
    headers: { Accept: "application/json", ...(options.headers || {}) },
  };
  if (state.token) {
    init.headers.Authorization = `Bearer ${state.token}`;
  }
  if (options.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = typeof options.body === "string" ? options.body : JSON.stringify(options.body);
  }
  const response = await fetch(path, init);
  if (response.status === 401) {
    const err = new Error("unauthorized");
    err.status = 401;
    throw err;
  }
  if (response.status === 403) {
    const err = new Error("permission denied");
    err.status = 403;
    throw err;
  }
  if (!response.ok) {
    const body = await response.text();
    const err = new Error(body || response.statusText);
    err.status = response.status;
    throw err;
  }
  const text = await response.text();
  return text ? JSON.parse(text) : null;
}

async function getOptional(path, fallback) {
  try {
    return await api(path);
  } catch (error) {
    if (error.status === 403) return fallback;
    throw error;
  }
}

function hasPermission(permission) {
  if (!state.principal || !Array.isArray(state.principal.permissions)) return false;
  return state.principal.permissions.includes("*") || state.principal.permissions.includes(permission);
}

function hasPermissions(permissions) {
  return permissions.split(/[\s,]+/).filter(Boolean).every(hasPermission);
}

function syncRBAC() {
  document.querySelectorAll("[data-perm]").forEach((node) => {
    const allowed = hasPermissions(node.dataset.perm);
    node.title = allowed ? "" : `Requires ${node.dataset.perm}`;
    if (node.classList.contains("nav-item") || node.classList.contains("driver-panel")) {
      node.hidden = !allowed;
    } else if ("disabled" in node) {
      node.disabled = !allowed;
    }
  });
}

function renderPrincipal() {
  const p = state.principal || {};
  const roles = Array.isArray(p.roles) && p.roles.length ? p.roles.join(", ") : "no role";
  document.getElementById("principal").textContent = `${p.display_name || p.actor_id || "unknown"} - ${roles}`;
  document.getElementById("oidc-state").textContent = p.auth_method === "oidc_session" ? "OIDC session" : p.auth_method || "session";
}

function renderFilters() {
  setOptions("group-filter", "All Groups", unique(state.devices.map((d) => d.group)));
  setOptions("vendor-filter", "All Vendors", unique(state.devices.map((d) => d.vendor)));
  setOptions("site-filter", "All Sites", unique(state.devices.map((d) => d.site).filter(Boolean)));
  setOptions("status-filter", "All Statuses", unique(state.devices.map((d) => deviceStatus(d).key)));
}

function setOptions(id, label, values) {
  const select = document.getElementById(id);
  const key = id.replace("-filter", "");
  const selected = state.filters[key] || "";
  select.innerHTML = `<option value="">${escapeHTML(label)}</option>` + values.map((value) => {
    return `<option value="${escapeAttr(value)}"${value === selected ? " selected" : ""}>${escapeHTML(value)}</option>`;
  }).join("");
}

function renderDevices() {
  const rows = document.getElementById("device-rows");
  const devices = filteredDevices();
  document.getElementById("device-count").textContent = `${devices.length} of ${state.devices.length} devices`;
  document.getElementById("empty-devices").hidden = devices.length > 0;
  rows.innerHTML = devices.map((device) => {
    const status = deviceStatus(device);
    const job = latestJob(device.id);
    return `
      <tr class="${device.id === state.selectedId ? "selected" : ""}" data-device="${escapeAttr(device.id)}">
        <td><span class="status-dot ${status.className}"></span>${escapeHTML(status.key)}</td>
        <td><span class="device-link">${escapeHTML(device.id)}</span></td>
        <td>${escapeHTML(device.group || "-")}</td>
        <td>${escapeHTML(device.vendor || "-")}</td>
        <td>${escapeHTML(device.site || "-")}</td>
        <td>${escapeHTML(relativeTime(jobTime(job)))}</td>
        <td><span class="result ${status.className}">${escapeHTML(status.label)}</span></td>
      </tr>`;
  }).join("");
  rows.querySelectorAll("tr").forEach((row) => {
    row.addEventListener("click", () => {
      state.selectedId = row.dataset.device;
      state.tab = "overview";
      renderDevices();
      renderDetail();
    });
  });
}

function filteredDevices() {
  return state.devices.filter((device) => {
    const status = deviceStatus(device).key;
    const q = state.filters.search;
    return (!state.filters.group || device.group === state.filters.group)
      && (!state.filters.vendor || device.vendor === state.filters.vendor)
      && (!state.filters.site || device.site === state.filters.site)
      && (!state.filters.status || status === state.filters.status)
      && (!q || [device.id, device.hostname, device.ip_address, device.vendor, device.group, device.site, device.role].join(" ").toLowerCase().includes(q));
  });
}

function renderSummary() {
  const counts = { success: 0, no_change: 0, queued: 0, running: 0, failed: 0, disabled: 0, unknown: 0 };
  state.devices.forEach((device) => {
    counts[deviceStatus(device).bucket] = (counts[deviceStatus(device).bucket] || 0) + 1;
  });
  const cards = [
    ["Success", counts.success, "success"],
    ["No Change", counts.no_change, "muted"],
    ["Queued", counts.queued, "queued"],
    ["Running", counts.running, "running"],
    ["Failed", counts.failed, "failed"],
  ];
  document.getElementById("summary-cards").innerHTML = cards.map(([label, count, className]) => {
    return `<div class="summary-card"><strong class="result ${className}">${count}</strong><span>${label}</span></div>`;
  }).join("");
  const ready = state.ready ? `${state.ready.status || "ready"}, queue depth ${state.ready.queue_depth ?? "unknown"}` : "Readiness unavailable";
  document.getElementById("runtime-status").textContent = ready;
}

function renderDetail() {
  syncRBAC();
  const device = selectedDevice();
  document.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === state.tab));
  if (!device) {
    document.getElementById("detail-title").textContent = "Select a device";
    document.getElementById("detail-status").textContent = "No selection";
    document.getElementById("detail-meta").innerHTML = "";
    document.getElementById("tab-content").innerHTML = `<div class="permission-note">Select a device from the status grid.</div>`;
    return;
  }
  const status = deviceStatus(device);
  const pill = document.getElementById("detail-status");
  document.getElementById("detail-title").textContent = device.id;
  pill.textContent = status.key;
  pill.className = `status-pill ${status.className}`;
  document.getElementById("detail-meta").innerHTML = [
    ["Group", device.group],
    ["Vendor", device.vendor],
    ["Site", device.site || "-"],
    ["Role", device.role || "-"],
  ].map(([label, value]) => `<div class="meta-cell"><span>${label}</span><strong>${escapeHTML(value || "-")}</strong></div>`).join("");

  if (state.tab === "config") return renderConfig(device);
  if (state.tab === "diff") return renderDiff(device);
  if (state.tab === "jobs") return renderJobs(device);
  renderOverview(device);
}

function renderOverview(device) {
  const job = latestJob(device.id);
  document.getElementById("tab-content").innerHTML = `
    <div class="detail-card">
      <p class="muted-label">Last Backup</p>
      <h3>${escapeHTML(relativeTime(jobTime(job)))}</h3>
      <p class="muted">${escapeHTML(job ? job.status : "No backup job recorded")}</p>
    </div>
    <div class="detail-actions">
      <button id="detail-trigger" class="button primary" type="button"${hasPermission("backups:run") ? "" : " disabled title=\"Requires backups:run\""}>Trigger Now</button>
      <button id="group-trigger" class="button secondary" type="button"${hasPermission("backups:run") ? "" : " disabled title=\"Requires backups:run\""}>Trigger Group</button>
    </div>
    <div class="job-json">${escapeHTML(JSON.stringify({
      id: device.id,
      hostname: device.hostname,
      ip_address: device.ip_address,
      port: device.port,
      enabled: device.enabled,
      telnet_enabled: device.telnet_enabled || false,
      jump_host: device.jump_host || "",
    }, null, 2))}</div>
  `;
  document.getElementById("detail-trigger").addEventListener("click", () => triggerDeviceBackup(device.id));
  document.getElementById("group-trigger").addEventListener("click", () => triggerGroupBackup(device.group));
}

async function renderConfig(device) {
  if (!hasPermission("configs:read")) {
    permissionNote("configs:read");
    return;
  }
  const target = document.getElementById("tab-content");
  target.innerHTML = `<div class="permission-note">Loading latest sanitized config...</div>`;
  try {
    if (!state.latestConfig.has(device.id)) {
      state.latestConfig.set(device.id, await api(`/api/v1/devices/${encodeURIComponent(device.id)}/configs/latest`));
    }
    const latest = state.latestConfig.get(device.id);
    target.innerHTML = `
      <div class="panel-title-row compact"><div><h3>Latest Config</h3><p>Sanitized content only</p></div></div>
      <pre class="code-block">${lineNumbered(latest.content || "")}</pre>
    `;
  } catch (error) {
    renderPanelError(error);
  }
}

async function renderDiff(device) {
  if (!hasPermissions("configs:read configs:diff")) {
    permissionNote("configs:read and configs:diff");
    return;
  }
  const target = document.getElementById("tab-content");
  target.innerHTML = `<div class="permission-note">Loading revisions...</div>`;
  try {
    if (!state.revisions.has(device.id)) {
      state.revisions.set(device.id, await api(`/api/v1/devices/${encodeURIComponent(device.id)}/configs?limit=20`));
    }
    const revisions = state.revisions.get(device.id) || [];
    if (revisions.length < 2) {
      target.innerHTML = `<div class="permission-note">At least two revisions are required to render a diff.</div>`;
      return;
    }
    const from = revisions[1].id;
    const to = revisions[0].id;
    target.innerHTML = `
      <div class="revision-controls">
        <label>From<select id="from-revision">${revisionOptions(revisions, from)}</select></label>
        <label>To<select id="to-revision">${revisionOptions(revisions, to)}</select></label>
        <button id="load-diff" class="button secondary" type="button">Load Diff</button>
      </div>
      <div id="diff-output" class="diff-block"></div>
    `;
    document.getElementById("load-diff").addEventListener("click", () => loadDiff(device.id));
    await loadDiff(device.id);
  } catch (error) {
    renderPanelError(error);
  }
}

async function loadDiff(deviceId) {
  const from = document.getElementById("from-revision").value;
  const to = document.getElementById("to-revision").value;
  const key = `${deviceId}:${from}:${to}`;
  const out = document.getElementById("diff-output");
  out.textContent = "Loading diff...";
  try {
    if (!state.diffs.has(key)) {
      state.diffs.set(key, await api(`/api/v1/devices/${encodeURIComponent(deviceId)}/configs/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`));
    }
    out.innerHTML = diffHTML(state.diffs.get(key).diff || "");
  } catch (error) {
    out.textContent = error.status === 403 ? "Requires configs:diff." : "Unable to load diff.";
  }
}

function renderJobs(device) {
  if (!hasPermission("jobs:read")) {
    permissionNote("jobs:read");
    return;
  }
  const jobs = state.jobs.filter((job) => job.target_id === device.id);
  if (!jobs.length) {
    document.getElementById("tab-content").innerHTML = `<div class="permission-note">No jobs recorded for this device.</div>`;
    return;
  }
  document.getElementById("tab-content").innerHTML = `
    <div class="job-list">
      ${jobs.slice(0, 12).map((job) => `
        <button class="job-row" type="button" data-job="${escapeAttr(job.id)}">
          <strong>${escapeHTML(job.id)}</strong>
          <p class="muted">${escapeHTML(job.status)} - ${escapeHTML(relativeTime(jobTime(job)))}</p>
        </button>
      `).join("")}
    </div>
    <pre id="job-detail" class="job-json" hidden></pre>
  `;
  document.querySelectorAll(".job-row").forEach((row) => {
    row.addEventListener("click", () => loadJobDetail(row.dataset.job));
  });
}

async function loadJobDetail(jobId) {
  const target = document.getElementById("job-detail");
  target.hidden = false;
  target.textContent = "Loading job detail...";
  try {
    if (!state.jobDetails.has(jobId)) {
      state.jobDetails.set(jobId, await api(`/api/v1/jobs/${encodeURIComponent(jobId)}`));
    }
    target.textContent = JSON.stringify(state.jobDetails.get(jobId), null, 2);
  } catch (error) {
    target.textContent = error.status === 403 ? "Requires jobs:read." : "Unable to load job detail.";
  }
}

function renderDrivers() {
  const panel = document.querySelector(".driver-panel");
  if (!hasPermission("drivers:read")) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  document.getElementById("drivers-copy").textContent = `${state.drivers.length} registered drivers`;
  document.getElementById("driver-list").innerHTML = state.drivers.map((driver) => {
    return `<button class="driver-chip ${driver === state.selectedDriver ? "active" : ""}" type="button" data-driver="${escapeAttr(driver)}">${escapeHTML(driver)}</button>`;
  }).join("");
  document.querySelectorAll(".driver-chip").forEach((chip) => {
    chip.addEventListener("click", () => {
      state.selectedDriver = chip.dataset.driver;
      renderDrivers();
    });
  });
}

async function triggerDeviceBackup(id) {
  if (!hasPermission("backups:run")) return showToast("Requires backups:run.");
  try {
    const res = await api(`/api/v1/devices/${encodeURIComponent(id)}/backup`, { method: "POST" });
    showToast(`Backup ${res.status || "queued"} for ${id}.`);
    await loadDashboard({ silent: true });
  } catch (error) {
    handleAPIError(error);
  }
}

async function triggerGroupBackup(group) {
  if (!hasPermission("backups:run")) return showToast("Requires backups:run.");
  if (!group) return showToast("Selected device has no group.");
  try {
    const res = await api(`/api/v1/groups/${encodeURIComponent(group)}/backup`, { method: "POST" });
    showToast(`Group ${group}: queued ${res.queued ?? 0}.`);
    await loadDashboard({ silent: true });
  } catch (error) {
    handleAPIError(error);
  }
}

async function reloadInventory() {
  if (!hasPermission("inventory:reload")) return showToast("Requires inventory:reload.");
  try {
    await api("/api/v1/inventory/reload", { method: "POST" });
    showToast("Inventory reloaded.");
    await loadDashboard({ silent: true });
  } catch (error) {
    handleAPIError(error);
  }
}

async function testSelectedDriver() {
  if (!hasPermission("drivers:test")) return showToast("Requires drivers:test.");
  if (!state.selectedDriver) return showToast("Select a driver first.");
  try {
    const res = await api(`/api/v1/drivers/${encodeURIComponent(state.selectedDriver)}/test`, { method: "POST" });
    showToast(`${state.selectedDriver}: ${res.status || "test accepted"}.`);
  } catch (error) {
    handleAPIError(error);
  }
}

async function logout() {
  try {
    await api("/auth/logout", { method: "POST" });
  } catch (_) {
    // Local token cleanup still makes the browser return to a safe state.
  }
  sessionStorage.removeItem("goxidized_token");
  state.token = "";
  state.principal = null;
  showLogin("");
}

function selectedDevice() {
  return state.devices.find((device) => device.id === state.selectedId) || null;
}

function latestJob(deviceId) {
  return state.jobs
    .filter((job) => job.target_id === deviceId)
    .sort((a, b) => new Date(jobTime(b) || 0) - new Date(jobTime(a) || 0))[0] || null;
}

function jobTime(job) {
  return job ? (job.updated_at || job.started_at || job.queued_at || "") : "";
}

function deviceStatus(device) {
  if (!device.enabled) return { key: "disabled", label: "Disabled", bucket: "disabled", className: "muted" };
  const job = latestJob(device.id);
  const status = job ? job.status : "unknown";
  if (status === "success_no_change") return { key: status, label: "No Change", bucket: "no_change", className: "muted" };
  if (status && status.startsWith("success")) return { key: status, label: "Changed", bucket: "success", className: "success" };
  if (status === "queued" || status === "leased") return { key: status, label: "Queued", bucket: "queued", className: "queued" };
  if (status === "running") return { key: status, label: "Running", bucket: "running", className: "running" };
  if (status && status.startsWith("failed")) return { key: status, label: status.replace("failed_", "Failed "), bucket: "failed", className: "failed" };
  return { key: "unknown", label: "Unknown", bucket: "unknown", className: "muted" };
}

function permissionNote(permission) {
  document.getElementById("tab-content").innerHTML = `<div class="permission-note">Requires ${escapeHTML(permission)}.</div>`;
}

function renderPanelError(error) {
  if (error.status === 401) {
    showLogin("");
    return;
  }
  const message = error.status === 403 ? "Permission denied." : "Unable to load this panel.";
  document.getElementById("tab-content").innerHTML = `<div class="permission-note">${message}</div>`;
}

function handleAPIError(error) {
  if (error.status === 401) {
    sessionStorage.removeItem("goxidized_token");
    state.token = "";
    showLogin("");
    return;
  }
  showToast(error.status === 403 ? "Permission denied." : `Request failed (${error.status || "network"}).`);
}

function revisionOptions(revisions, selected) {
  return revisions.map((rev) => {
    const label = `${rev.id} ${rev.commit_sha ? rev.commit_sha.slice(0, 8) : ""}`;
    return `<option value="${escapeAttr(rev.id)}"${rev.id === selected ? " selected" : ""}>${escapeHTML(label)}</option>`;
  }).join("");
}

function lineNumbered(content) {
  return String(content).split("\n").map((line, index) => {
    return `<span class="code-line"><span class="line-no">${String(index + 1).padStart(2, " ")}</span>${escapeHTML(line)}</span>`;
  }).join("");
}

function diffHTML(content) {
  return String(content || "").split("\n").map((line) => {
    const className = line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : line.startsWith("@@") ? "hunk" : "";
    return `<span class="diff-line ${className}">${escapeHTML(line || " ")}</span>`;
  }).join("");
}

function relativeTime(value) {
  if (!value) return "-";
  const ts = new Date(value).getTime();
  if (Number.isNaN(ts)) return "-";
  const seconds = Math.max(0, Math.round((Date.now() - ts) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

function unique(values) {
  return [...new Set(values.filter(Boolean))].sort((a, b) => String(a).localeCompare(String(b)));
}

function showToast(message) {
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => {
    toast.hidden = true;
  }, 3200);
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[char]));
}

function escapeAttr(value) {
  return escapeHTML(value).replace(/`/g, "&#96;");
}
