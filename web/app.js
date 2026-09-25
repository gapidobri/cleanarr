"use strict";

const CATS = [
  { id: "untracked_folder", label: "Untracked folders", short: "Untracked folder" },
  { id: "untracked_file", label: "Untracked files", short: "Untracked file" },
  { id: "unused_torrent", label: "Unused torrents", short: "Unused torrent" },
  { id: "download_leftover", label: "Download leftovers", short: "Leftover" },
  { id: "recycle_bin", label: "Recycle bin", short: "Recycle bin" },
];
const CAT = Object.fromEntries(CATS.map((c) => [c.id, c]));

const state = {
  route: "cleanup",
  status: null,
  items: [],
  itemsLoaded: false,
  config: null,
  jobs: [],
  selected: new Set(),
  expanded: new Set(),
  hiddenCats: new Set(),
  query: "",
  sort: { key: "size", dir: -1 },
  expandedJob: null,
  jobDetail: null,
};

/* ---------- helpers ---------- */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const icon = (id) => `<svg aria-hidden="true"><use href="#i-${id}"/></svg>`;
const plural = (n, one, many = one + "s") => `${n.toLocaleString()} ${n === 1 ? one : many}`;

function fmtBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
function timeAgo(iso) {
  if (!iso) return "";
  const s = (new Date(iso) - Date.now()) / 1000;
  const steps = [[60, "second"], [60, "minute"], [24, "hour"], [30, "day"], [12, "month"], [Infinity, "year"]];
  let v = s;
  for (const [n, unit] of steps) {
    if (Math.abs(v) < n) return rtf.format(Math.round(v), unit);
    v /= n;
  }
}
function fmtDate(iso) {
  if (!iso) return "";
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}
function fmtDuration(a, b) {
  const s = Math.max(0, Math.round((new Date(b) - new Date(a)) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function toast(html, kind = "") {
  const el = document.createElement("div");
  el.className = `toast ${kind}`;
  el.innerHTML = html;
  $("#toasts").append(el);
  setTimeout(() => el.remove(), kind === "err" ? 9000 : 5000);
}

/* ---------- data ---------- */

async function loadStatus() {
  const prev = state.status;
  try {
    state.status = await api("GET", "/api/status");
  } catch {
    return;
  }
  const s = state.status;
  renderScanState();
  const badge = $("#active-badge");
  badge.hidden = !s.activeJobs;
  badge.textContent = s.activeJobs;

  const scanDone = prev && prev.scan.running && !s.scan.running;
  const jobsDone = prev && prev.activeJobs > 0 && s.activeJobs === 0;
  if (scanDone) {
    if (s.scan.error) toast(`Scan failed: ${esc(s.scan.error)}`, "err");
    else toast(`Scan finished. ${esc(s.scan.message)}.`);
  }
  if (scanDone || jobsDone) await loadItems();
  if (jobsDone) {
    await loadJobs();
    toast(`Deletion finished. <a href="#/activity">See what was deleted</a>`);
  }
  if (state.route === "activity" && (s.activeJobs > 0 || jobsDone)) await refreshActivity();
  if (scanDone || jobsDone || (prev && prev.scan.running !== s.scan.running)) render();
}

async function loadItems() {
  state.items = await api("GET", "/api/items");
  state.itemsLoaded = true;
  const ids = new Set(state.items.map((i) => i.id));
  for (const id of state.selected) if (!ids.has(id)) state.selected.delete(id);
}
async function loadConfig() { state.config = await api("GET", "/api/config"); }
async function loadJobs() { state.jobs = await api("GET", "/api/jobs"); }

function schedulePoll() {
  const busy = state.status && (state.status.scan.running || state.status.activeJobs > 0);
  setTimeout(async () => { await loadStatus(); schedulePoll(); }, busy ? 1500 : 5000);
}

function renderScanState() {
  const s = state.status;
  const el = $("#scan-state");
  if (!s) return;
  if (s.scan.running) {
    el.innerHTML = `<span class="spin"></span><span class="msg">${esc(s.scan.message)}</span>`;
  } else if (s.lastScan) {
    el.innerHTML = `<span class="msg">Scanned ${esc(timeAgo(s.lastScan.finishedAt))}</span>`;
  } else {
    el.innerHTML = "";
  }
}

async function startScan() {
  try {
    await api("POST", "/api/scan");
    await loadStatus();
    render();
  } catch (e) {
    toast(esc(e.message), "err");
  }
}

/* ---------- routing ---------- */

const routes = { "": "cleanup", activity: "activity", settings: "settings", system: "system" };

async function route() {
  const key = location.hash.replace(/^#\/?/, "").split("/")[0];
  state.route = routes[key] || "cleanup";
  for (const a of $$(".rail a")) {
    if (a.dataset.route === state.route) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  $("#rail").classList.remove("open");
  if ($("#modal-root").firstChild) closeModal();
  $(".search").style.visibility = state.route === "cleanup" ? "" : "hidden";
  if (state.route === "activity") await loadJobs();
  if (state.route === "settings" || !state.config) await loadConfig();
  render();
  window.scrollTo(0, 0);
}

function render() {
  const view = $("#view");
  const fn = { cleanup: renderCleanup, activity: renderActivity, settings: renderSettings, system: renderSystem }[state.route];
  view.innerHTML = fn();
  if (state.route === "cleanup") updateSelection();
}

/* ---------- cleanup ---------- */

function visibleItems() {
  const q = state.query.trim().toLowerCase();
  const list = state.items.filter((it) => {
    if (state.hiddenCats.has(it.category)) return false;
    if (!q) return true;
    return [it.name, it.path, it.related, it.instance].some((v) => v && v.toLowerCase().includes(q));
  });
  const { key, dir } = state.sort;
  const val = (it) => {
    switch (key) {
      case "name": return it.name.toLowerCase();
      case "related": return (it.related || "~").toLowerCase();
      case "category": return CATS.findIndex((c) => c.id === it.category);
      case "modTime": return new Date(it.modTime).getTime();
      default: return it[key];
    }
  };
  return list.sort((a, b) => {
    const x = val(a), y = val(b);
    return x < y ? -dir : x > y ? dir : 0;
  });
}

function volumes() {
  const roots = (state.status?.lastScan?.roots || []).filter((r) => r.total > 0);
  const byKey = new Map();
  for (const r of roots) {
    const key = `${r.total}/${r.free}`;
    if (!byKey.has(key)) byKey.set(key, { key, total: r.total, free: r.free, paths: [], byCat: {}, selByCat: {} });
    byKey.get(key).paths.push(r.path);
  }
  const vols = [...byKey.values()];
  if (!vols.length) return vols;
  const volOf = (it) => {
    let best = null, len = -1;
    for (const r of roots) {
      if ((it.path === r.path || it.path.startsWith(r.path + "/")) && r.path.length > len) {
        best = byKey.get(`${r.total}/${r.free}`);
        len = r.path.length;
      }
    }
    return best || vols[0];
  };
  for (const it of state.items) {
    const v = volOf(it);
    v.byCat[it.category] = (v.byCat[it.category] || 0) + it.reclaimable;
    if (state.selected.has(it.id)) v.selByCat[it.category] = (v.selByCat[it.category] || 0) + it.reclaimable;
  }
  return vols;
}

function commonDir(paths) {
  let parts = paths[0].split("/");
  for (const p of paths.slice(1)) {
    const q = p.split("/");
    let i = 0;
    while (i < parts.length && parts[i] === q[i]) i++;
    parts = parts.slice(0, i);
  }
  return parts.join("/") || "/";
}

function volumeHTML(v) {
  const used = v.total - v.free;
  const reclaim = Object.values(v.byCat).reduce((a, b) => a + b, 0);
  const pct = (n) => (n / v.total) * 100;
  const segs = CATS.filter((c) => v.byCat[c.id] > 0).map((c) => {
    const w = Math.max(pct(v.byCat[c.id]), 0.5);
    return `<div class="seg cat" data-cat="${c.id}" style="--cat:var(--c-${c.id});width:${w}%" title="${esc(c.label)}: ${fmtBytes(v.byCat[c.id])}"><i class="sel" style="width:0"></i></div>`;
  }).join("");
  const prefix = commonDir(v.paths);
  const names = v.paths.length === 1
    ? `<b>${esc(v.paths[0])}</b>`
    : `<b>${esc(prefix)}</b>, ${v.paths.length} scanned folders`;
  return `
    <div class="volume" data-vol="${esc(v.key)}">
      <div class="volume-head">
        <div class="volume-name" title="${esc(v.paths.join("\n"))}">${names}</div>
        <div class="volume-figures">
          <span class="fig-free">${fmtBytes(v.free)}<em>free of ${fmtBytes(v.total)}</em></span>
          <span class="fig-reclaim">${fmtBytes(reclaim)}<em>can be freed</em></span>
        </div>
      </div>
      <div class="bar" role="img" aria-label="${fmtBytes(used)} used, ${fmtBytes(reclaim)} can be freed, ${fmtBytes(v.free)} free">
        <div class="seg used" style="width:${Math.max(0, pct(used - reclaim))}%"></div>${segs}
      </div>
      <div class="volume-note"></div>
    </div>`;
}

function hasArrs() {
  const c = state.config;
  return c && [...c.sonarr, ...c.radarr].some((a) => a.enabled);
}

function renderCleanup() {
  const s = state.status;
  const scanning = s?.scan.running;
  const toolbar = `
    <div class="toolbar">
      <button class="tool" data-act="scan" ${scanning ? "disabled" : ""}>${icon("scan")}${scanning ? "Scanning" : "Scan now"}</button>
      <div class="tool-sep"></div>
      <button class="tool" data-act="select-all">${icon("select")}Select all</button>
      <button class="tool" data-act="clear" data-needs-sel>${icon("clear")}Clear</button>
      <button class="tool danger" data-act="delete" data-needs-sel>${icon("trash")}Delete</button>
    </div>`;

  if (!hasArrs()) {
    return toolbar.replace('data-act="scan"', 'data-act="scan" disabled') + `
      <div class="page"><div class="empty">
        <h2>Connect Sonarr or Radarr</h2>
        <p>Cleanarr compares your library folders with what Sonarr and Radarr track. Add at least one instance to scan.</p>
        <a class="btn primary" href="#/settings">Open settings</a>
      </div></div>`;
  }
  if (!s?.lastScan) {
    return toolbar + `
      <div class="page"><div class="empty">
        <h2>${scanning ? "Scanning your library" : "Scan your library"}</h2>
        <p>${scanning ? esc(s.scan.message) + ". Large libraries on NFS can take a few minutes." : "A scan reads every file in the Sonarr and Radarr root folders and your download paths. Nothing is deleted until you confirm."}</p>
        ${scanning ? "" : `<button class="btn primary" data-act="scan">Scan now</button>`}
        ${s?.scan.error ? `<p style="color:var(--danger);margin-top:16px">Last scan failed: ${esc(s.scan.error)}</p>` : ""}
      </div></div>`;
  }

  const vols = volumes();
  const counts = {};
  for (const it of state.items) {
    counts[it.category] ??= { n: 0, size: 0 };
    counts[it.category].n++;
    counts[it.category].size += it.size;
  }
  const chips = CATS.filter((c) => counts[c.id]).map((c) => `
    <button class="cat" data-cat="${c.id}" aria-pressed="${!state.hiddenCats.has(c.id)}" style="--cat:var(--c-${c.id})">
      <span class="sw"></span>${esc(c.label)}<span class="n">${counts[c.id].n}, ${fmtBytes(counts[c.id].size)}</span>
    </button>`).join("");

  const items = visibleItems();
  let body;
  if (!state.items.length) {
    body = `<div class="empty"><h2>Nothing to clean up</h2><p>Every file in the scanned paths is used by Sonarr, Radarr or a torrent.</p></div>`;
  } else if (!items.length) {
    body = `<div class="empty"><h2>No matches</h2><p>No items match the filter. Clear the search or turn categories back on.</p></div>`;
  } else {
    const th = (key, label, cls = "") => {
      const sorted = state.sort.key === key ? ` aria-sort="${state.sort.dir > 0 ? "ascending" : "descending"}"` : "";
      return `<th class="sortable ${cls}" data-sort="${key}"${sorted}>${label}</th>`;
    };
    body = `
      <div class="table-wrap"><table>
        <thead><tr>
          <th class="check"><input type="checkbox" id="check-all" aria-label="Select all shown"></th>
          <th style="width:4px;padding:0"></th>
          ${th("name", "Name")}
          ${th("related", "Belongs to", "col-rel")}
          ${th("category", "Type", "col-kind")}
          <th class="col-marks">Held by</th>
          ${th("modTime", "Modified", "num col-mod")}
          ${th("size", "Size", "num")}
          ${th("reclaimable", "Frees", "num")}
        </tr></thead>
        <tbody>${items.map(rowHTML).join("")}</tbody>
      </table></div>`;
  }

  return toolbar + `
    <div class="page">
      <h1>Cleanup</h1>
      <p class="lede">Files on your NAS that Sonarr and Radarr no longer use. Old versions from upgrades show up as untracked files next to the new one, or as unused torrents that are still seeding.</p>
      ${vols.length ? `<section class="volumes">${vols.map(volumeHTML).join("")}</section>` : ""}
      <div class="cats">${chips}</div>
      ${body}
    </div>
    <div class="selbar" id="selbar" hidden>
      <span class="what" id="sel-what"></span>
      <span class="gainline" id="sel-gain"></span>
      <span class="spacer"></span>
      <button class="btn ghost" data-act="clear">Clear</button>
      <button class="btn danger" data-act="delete">Preview deletion</button>
    </div>`;
}

// shortDir shows where an item lives relative to the scanned folder that holds
// it, e.g. "movies/Dune (2021)" instead of the full path.
function shortDir(it) {
  const full = it.category === "unused_torrent" && it.isDir ? it.path : it.path.slice(0, it.path.lastIndexOf("/")) || "/";
  let root = "";
  for (const r of state.status?.lastScan?.roots || []) {
    if ((full === r.path || full.startsWith(r.path + "/")) && r.path.length > root.length) root = r.path;
  }
  if (!root) return full;
  const base = root.slice(root.lastIndexOf("/") + 1);
  return base + full.slice(root.length);
}

function rowHTML(it) {
  const cat = CAT[it.category];
  const sel = state.selected.has(it.id);
  const marks = [];
  if (it.torrents.length) marks.push(`<span class="mark" title="${esc(it.torrents.map((t) => t.name).join("\n"))}">${icon("torrent")}${plural(it.torrents.length, "torrent")}</span>`);
  if (it.extraLinks.length) marks.push(`<span class="mark" title="${esc(it.extraLinks.join("\n"))}">${icon("link")}${plural(it.extraLinks.length, "hardlink")}</span>`);
  if (it.sharesTracked) marks.push(`<span class="mark warn">${icon("alert")}Library uses data</span>`);
  if (it.blocked.length) marks.push(`<span class="mark warn" title="${esc(it.blocked.join("\n"))}">${icon("alert")}Kept torrent</span>`);
  if (it.unknownLinks) marks.push(`<span class="mark warn">${icon("alert")}${plural(it.unknownLinks, "link")} not found</span>`);
  const dir = shortDir(it);
  let html = `
    <tr class="row${sel ? " selected" : ""}" data-id="${it.id}" aria-expanded="${state.expanded.has(it.id)}">
      <td class="check"><input type="checkbox" data-sel ${sel ? "checked" : ""} aria-label="Select ${esc(it.name)}"></td>
      <td class="stripe" style="--cat:var(--c-${it.category})"><i></i></td>
      <td><div class="name">${esc(it.name)}</div><div class="sub">${esc(dir)}</div></td>
      <td class="rel col-rel">${it.related ? `${esc(it.related)}<div class="sub">${esc(it.instance)}</div>` : ""}</td>
      <td class="kind col-kind">${esc(cat?.short || it.category)}</td>
      <td class="col-marks"><div class="marks">${marks.join("")}</div></td>
      <td class="num col-mod muted" title="${esc(fmtDate(it.modTime))}">${esc(timeAgo(it.modTime))}</td>
      <td class="num">${fmtBytes(it.size)}</td>
      <td class="num frees${it.reclaimable ? "" : " none"}">${it.reclaimable ? fmtBytes(it.reclaimable) : "Nothing"}</td>
    </tr>`;
  if (state.expanded.has(it.id)) html += detailHTML(it);
  return html;
}

function detailHTML(it) {
  const list = (arr) => `<ul>${arr.map((x) => `<li>${x}</li>`).join("")}</ul>`;
  const rows = [
    ["Why it is listed", esc(it.reason)],
    ["Path", esc(it.path)],
    ["Files", `${it.fileCount.toLocaleString()}${it.isDir ? " in folder" : ""}`],
  ];
  if (it.torrents.length) rows.push(["Torrents removed with it", list(it.torrents.map((t) => `${esc(t.name)} <span class="faint">in ${esc(t.clientName)}${t.category ? `, ${esc(t.category)}` : ""}</span>`))]);
  if (it.extraLinks.length) rows.push(["Other hardlinks", list(it.extraLinks.map(esc))]);
  if (it.blocked.length) rows.push(["Torrents kept", list(it.blocked.map(esc))]);
  if (it.sharesTracked) rows.push(["Note", "Some of this data is also used by a tracked file, so deleting it frees no space for that part."]);
  if (it.unknownLinks) rows.push(["Note", `${plural(it.unknownLinks, "hardlink")} could not be found in the scanned paths. Add the folder that holds them as a download path in Settings, or the space stays in use.`]);
  return `<tr class="detail"><td colspan="9"><div class="detail"><dl>${rows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("")}</dl></div></td></tr>`;
}

function selectedItems() {
  return state.items.filter((it) => state.selected.has(it.id));
}

// updateSelection refreshes everything that depends on the selection without
// re-rendering the table, so the bars can animate.
function updateSelection() {
  if (state.route !== "cleanup") return;
  const sel = selectedItems();
  const size = sel.reduce((a, it) => a + it.size, 0);
  const gain = sel.reduce((a, it) => a + it.reclaimable, 0);

  for (const tr of $$("tr.row")) {
    const on = state.selected.has(tr.dataset.id);
    tr.classList.toggle("selected", on);
    $("input[data-sel]", tr).checked = on;
  }
  const all = $("#check-all");
  if (all) {
    const shown = visibleItems();
    const n = shown.filter((it) => state.selected.has(it.id)).length;
    all.checked = n > 0 && n === shown.length;
    all.indeterminate = n > 0 && n < shown.length;
  }
  for (const b of $$("[data-needs-sel]")) b.disabled = sel.length === 0;

  for (const v of volumes()) {
    const el = $$(".volume").find((x) => x.dataset.vol === v.key);
    if (!el) continue;
    for (const seg of $$(".seg.cat", el)) {
      const c = seg.dataset.cat;
      const part = v.byCat[c] ? (v.selByCat[c] || 0) / v.byCat[c] : 0;
      $(".sel", seg).style.width = `${part * 100}%`;
    }
    const selGain = Object.values(v.selByCat).reduce((a, b) => a + b, 0);
    $(".volume-note", el).innerHTML = selGain
      ? `<span class="gainline">The selection frees ${fmtBytes(selGain)}</span>, leaving ${fmtBytes(v.free + selGain)} free.`
      : "Select items to see how much space they free on this volume.";
  }

  const bar = $("#selbar");
  if (bar) {
    bar.hidden = sel.length === 0;
    $("#sel-what").textContent = `${plural(sel.length, "item")} selected, ${fmtBytes(size)}`;
    $("#sel-gain").textContent = gain ? `frees ${fmtBytes(gain)}` : "frees no space";
  }
}

/* ---------- delete flow ---------- */

const deleteOpts = { removeTorrents: true, removeLinks: true };

async function openDelete() {
  const ids = [...state.selected];
  if (!ids.length) return;
  showModal(`<div class="modal-head"><h2>Preview deletion</h2></div><div class="modal-body"><p class="muted">Working out what will be deleted…</p></div>`);
  let plan;
  try {
    plan = await api("POST", "/api/plan", { ids, options: deleteOpts });
  } catch (e) {
    showModal(`<div class="modal-head"><h2>Preview deletion</h2></div><div class="modal-body"><p style="color:var(--danger)">${esc(e.message)}</p></div><div class="modal-foot"><button class="btn" data-act="close">Close</button></div>`);
    return;
  }
  const li = (arr, fn) => arr.map((x) => `<li>${fn(x)}</li>`).join("");
  const torrentsWithExtra = plan.torrents.filter((t) => t.otherFiles > 0).length;
  showModal(`
    <div class="modal-head"><h2>Delete ${plural(plan.items.length, "item")}</h2><button class="btn ghost" data-act="close" aria-label="Close">Close</button></div>
    <div class="modal-body">
      <dl class="plan-figures">
        <div><dt>Items</dt><dd>${plan.items.length.toLocaleString()}</dd></div>
        <div><dt>Files</dt><dd>${plan.files.toLocaleString()}</dd></div>
        <div><dt>Torrents</dt><dd>${plan.torrents.length.toLocaleString()}</dd></div>
        <div><dt>Hardlinks</dt><dd>${plan.links.length.toLocaleString()}</dd></div>
        <div class="gain"><dt>Space freed</dt><dd>${fmtBytes(plan.freed)}</dd></div>
      </dl>
      <div class="options">
        <div class="check-field">
          <input type="checkbox" id="opt-torrents" ${plan.options.removeTorrents ? "checked" : ""}>
          <div><label for="opt-torrents">Remove the torrents from qBittorrent</label>
          <div class="help">Stops seeding and deletes the torrent data. Without this, files that a torrent still holds are kept.</div></div>
        </div>
        <div class="check-field">
          <input type="checkbox" id="opt-links" ${plan.options.removeLinks ? "checked" : ""}>
          <div><label for="opt-links">Delete other hardlinks of the same files</label>
          <div class="help">A file only frees space once every hardlink to it is gone. Links shared with tracked media are never touched.</div></div>
        </div>
      </div>
      ${plan.warnings.length ? `<ul class="plan-warnings">${li(plan.warnings, esc)}</ul>` : ""}
      <details class="plan-list"><summary>${plural(plan.items.length, "item")}, ${fmtBytes(plan.size)}</summary><ul>${li(plan.items, (i) => `${esc(i.path)} <span class="faint">${fmtBytes(i.size)}</span>`)}</ul></details>
      ${plan.torrents.length ? `<details class="plan-list"${torrentsWithExtra ? " open" : ""}><summary>${plural(plan.torrents.length, "torrent")} removed from qBittorrent</summary><ul>${li(plan.torrents, (t) => `${esc(t.name)} <span class="faint">in ${esc(t.clientName)}${t.otherFiles ? `, plus ${plural(t.otherFiles, "other file")}` : ""}</span>`)}</ul></details>` : ""}
      ${plan.links.length ? `<details class="plan-list"><summary>${plural(plan.links.length, "hardlink")} deleted</summary><ul>${li(plan.links, esc)}</ul></details>` : ""}
    </div>
    <div class="modal-foot">
      <span class="note">Deletion is permanent. It runs in the background.</span>
      <button class="btn" data-act="close">Cancel</button>
      <button class="btn danger" data-act="confirm-delete">Delete ${plural(plan.items.length, "item")}</button>
    </div>`);
  $("#opt-torrents").onchange = (e) => { deleteOpts.removeTorrents = e.target.checked; openDelete(); };
  $("#opt-links").onchange = (e) => { deleteOpts.removeLinks = e.target.checked; openDelete(); };
}

async function confirmDelete(btn) {
  btn.disabled = true;
  try {
    const job = await api("POST", "/api/jobs", { ids: [...state.selected], options: deleteOpts });
    closeModal();
    state.selected.clear();
    updateSelection();
    toast(`Deleting ${plural(job.items.length, "item")} in the background. <a href="#/activity">Follow progress</a>`);
    await loadStatus();
  } catch (e) {
    btn.disabled = false;
    toast(esc(e.message), "err");
  }
}

/* ---------- modal ---------- */

let lastFocus = null;
function showModal(html) {
  const root = $("#modal-root");
  if (!root.firstChild) lastFocus = document.activeElement;
  root.innerHTML = `<div class="backdrop" data-act="backdrop"><div class="modal" role="dialog" aria-modal="true">${html}</div></div>`;
  const focusable = $(".modal [data-act='confirm-delete'], .modal input, .modal button", root);
  focusable?.focus();
}
function closeModal() {
  $("#modal-root").innerHTML = "";
  lastFocus?.focus?.();
}

/* ---------- activity ---------- */

const STATUS_LABEL = { queued: "Queued", running: "Running", completed: "Completed", partial: "Finished with problems", failed: "Failed", interrupted: "Interrupted" };

async function refreshActivity() {
  await loadJobs();
  if (state.expandedJob) state.jobDetail = await api("GET", `/api/jobs/${state.expandedJob}`).catch(() => null);
  if (state.route === "activity" && !$("#modal-root").firstChild) render();
}

function renderActivity() {
  const jobs = state.jobs;
  if (!jobs.length) {
    return `<div class="page"><h1>Activity</h1><div class="empty"><h2>No deletions yet</h2><p>When you delete items on the Cleanup page, the job and its log show up here.</p><a class="btn" href="#/">Go to Cleanup</a></div></div>`;
  }
  const rows = jobs.map((j) => {
    const first = j.items[0];
    const what = first ? `${esc(first.related || first.name)}${j.items.length > 1 ? ` <span class="faint">and ${j.items.length - 1} more</span>` : ""}` : "";
    const active = j.status === "running" || j.status === "queued";
    const pct = j.total ? Math.round((j.done / j.total) * 100) : 0;
    const result = active
      ? `${esc(j.step || "Waiting")}<div class="progress"><i style="width:${pct}%"></i></div>`
      : `${fmtBytes(j.removedBytes)} freed, ${plural(j.removedFiles, "file")}${j.removedTorrents ? `, ${plural(j.removedTorrents, "torrent")}` : ""}`;
    let html = `
      <tr class="row" data-job="${j.id}" aria-expanded="${state.expandedJob === j.id}">
        <td><span class="status ${j.status}">${STATUS_LABEL[j.status] || j.status}</span></td>
        <td><div class="name">${what}</div><div class="sub">${plural(j.items.length, "item")}, ${fmtBytes(j.size)}</div></td>
        <td>${result}</td>
        <td class="num">${j.errors ? `<span style="color:var(--danger)">${plural(j.errors, "error")}</span>` : ""}${j.skipped ? ` <span style="color:var(--warn)">${j.skipped} kept</span>` : ""}</td>
        <td class="num muted" title="${esc(fmtDate(j.createdAt))}">${esc(timeAgo(j.createdAt))}</td>
      </tr>`;
    if (state.expandedJob === j.id) {
      const d = state.jobDetail?.id === j.id ? state.jobDetail : null;
      const opts = [j.options.removeTorrents ? "torrents removed" : "torrents kept", j.options.removeLinks ? "hardlinks deleted" : "hardlinks kept"].join(", ");
      html += `<tr class="detail"><td colspan="5"><div class="detail"><dl>
        <dt>Options</dt><dd>${opts}</dd>
        ${j.startedAt ? `<dt>Ran</dt><dd>${esc(fmtDate(j.startedAt))}${j.finishedAt ? `, took ${fmtDuration(j.startedAt, j.finishedAt)}` : ""}</dd>` : ""}
        <dt>Log</dt><dd>${d ? `<ul class="log">${d.log.map((l) => `<li><time>${new Date(l.time).toLocaleTimeString()}</time><span class="${l.level}">${esc(l.msg)}</span></li>`).join("") || "<li>No entries yet</li>"}</ul>` : "Loading…"}</dd>
      </dl></div></td></tr>`;
    }
    return html;
  }).join("");
  return `<div class="page">
    <h1>Activity</h1>
    <p class="lede">Deletion jobs run one at a time. Before deleting, each job re-reads Sonarr and Radarr and skips anything that became tracked since the scan.</p>
    <div class="table-wrap"><table>
      <thead><tr><th>Status</th><th>Items</th><th>Result</th><th class="num">Problems</th><th class="num">Created</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div></div>`;
}

/* ---------- settings ---------- */

const KIND_LABEL = { sonarr: "Sonarr", radarr: "Radarr", qbit: "qBittorrent" };

function instanceList(kind, list) {
  const head = `<div class="section-head"><h2>${KIND_LABEL[kind]}</h2><button class="btn" data-act="add-instance" data-kind="${kind}">${icon("plus")}Add ${KIND_LABEL[kind]}</button></div>`;
  if (!list.length) {
    const hint = kind === "qbit"
      ? "Connect qBittorrent to find old versions that are still seeding and to remove torrents together with their files."
      : `No ${KIND_LABEL[kind]} instance yet.`;
    return head + `<div class="none-yet">${hint}</div>`;
  }
  return head + `<div class="instances">${list.map((inst, i) => `
    <div class="instance${inst.enabled ? "" : " off"}">
      <span class="dot" title="${inst.enabled ? "Enabled" : "Disabled"}"></span>
      <div class="meta"><div class="name">${esc(inst.name)}</div><div class="sub">${esc(inst.url)}${kind === "qbit" && inst.categories.length ? `, categories ${esc(inst.categories.join(", "))}` : ""}${inst.enabled ? "" : ", disabled"}</div></div>
      <button class="btn" data-act="edit-instance" data-kind="${kind}" data-index="${i}">Edit</button>
    </div>`).join("")}</div>`;
}

const lines = (arr) => esc((arr || []).join("\n"));
const parseLines = (s) => s.split("\n").map((x) => x.trim()).filter(Boolean);

function renderSettings() {
  const c = state.config;
  if (!c) return `<div class="page"><p class="muted">Loading…</p></div>`;
  return `<div class="page">
    <h1>Settings</h1>
    <p class="lede">Cleanarr sees the same paths as Sonarr and Radarr. If qBittorrent runs in a container with different paths, add a path mapping to it.</p>
    ${instanceList("sonarr", c.sonarr)}
    ${instanceList("radarr", c.radarr)}
    ${instanceList("qbit", c.qbittorrent)}

    <h2 style="margin-top:40px">Paths</h2>
    <form class="form" id="paths-form">
      <div class="field">
        <label for="f-downloads">Download paths</label>
        <textarea id="f-downloads" name="downloadPaths" spellcheck="false" placeholder="/data/torrents">${lines(c.downloadPaths)}</textarea>
        <div class="help">Where qBittorrent and other download clients save files. Used to find hardlinks of library files and downloads nothing uses any more. One path per line.</div>
      </div>
      <div class="field">
        <label for="f-extra">Extra library paths</label>
        <textarea id="f-extra" name="extraLibraryPaths" spellcheck="false">${lines(c.extraLibraryPaths)}</textarea>
        <div class="help">Scanned in addition to the root folders from Sonarr and Radarr, which are included automatically.</div>
      </div>
      <div class="field">
        <label for="f-excluded">Excluded paths</label>
        <textarea id="f-excluded" name="excludedPaths" spellcheck="false">${lines(c.excludedPaths)}</textarea>
        <div class="help">Never listed and never deleted, including everything inside.</div>
      </div>
      <div class="field">
        <label for="f-ignored">Ignored names</label>
        <textarea id="f-ignored" name="ignoredNames" spellcheck="false">${lines(c.ignoredNames)}</textarea>
        <div class="help">Files and folders with these exact names are skipped, such as Synology's @eaDir.</div>
      </div>
      <h2>Scanning</h2>
      <div class="row-fields">
        <div class="field">
          <label for="f-minage">Ignore files newer than (hours)</label>
          <input type="number" id="f-minage" name="minAgeHours" min="0" value="${c.minAgeHours}">
          <div class="help">Protects downloads and imports that are still in progress.</div>
        </div>
        <div class="field">
          <label for="f-interval">Scan automatically every (hours)</label>
          <input type="number" id="f-interval" name="scanIntervalHours" min="0" value="${c.scanIntervalHours}">
          <div class="help">0 turns automatic scans off.</div>
        </div>
      </div>
      <div class="check-field">
        <input type="checkbox" id="f-recycle" name="includeRecycleBins" ${c.includeRecycleBins ? "checked" : ""}>
        <label for="f-recycle">List what is in the Sonarr and Radarr recycle bins</label>
      </div>
      <div class="form-actions"><button class="btn primary" type="submit">Save settings</button></div>
    </form>
  </div>`;
}

function instanceForm(kind, inst) {
  const isQ = kind === "qbit";
  const maps = (inst.pathMappings || []).map((m) => `${m.remote} => ${m.local}`).join("\n");
  return `
    <form id="instance-form">
      <div class="modal-head"><h2>${inst.id ? "Edit" : "Add"} ${KIND_LABEL[kind]}</h2></div>
      <div class="modal-body">
        <div class="row-fields">
          <div class="field"><label for="i-name">Name</label><input type="text" id="i-name" name="name" required value="${esc(inst.name)}" placeholder="${isQ ? "qBittorrent" : KIND_LABEL[kind] + " 4K"}"></div>
          <div class="field"><label for="i-url">URL</label><input type="url" id="i-url" name="url" required value="${esc(inst.url)}" placeholder="http://192.168.1.10:${kind === "sonarr" ? 8989 : kind === "radarr" ? 7878 : 8080}"></div>
        </div>
        ${isQ ? `
        <div class="row-fields">
          <div class="field"><label for="i-user">Username</label><input type="text" id="i-user" name="username" value="${esc(inst.username)}" autocomplete="off"></div>
          <div class="field"><label for="i-pass">Password</label><input type="password" id="i-pass" name="password" value="${esc(inst.password)}" autocomplete="off"></div>
        </div>
        <div class="field"><label for="i-cats">Categories</label><input type="text" id="i-cats" name="categories" value="${esc((inst.categories || []).join(", "))}" placeholder="radarr, tv-sonarr">
          <div class="help">Only torrents in these categories are listed or removed. Leave empty to include every category.</div></div>
        <div class="field"><label for="i-maps">Path mappings</label><textarea id="i-maps" name="pathMappings" spellcheck="false" placeholder="/downloads => /data/torrents">${esc(maps)}</textarea>
          <div class="help">Only needed when qBittorrent sees different paths than Cleanarr. One mapping per line: qBittorrent path => Cleanarr path.</div></div>
        ` : `
        <div class="field"><label for="i-key">API key</label><input type="text" id="i-key" name="apiKey" required value="${esc(inst.apiKey)}" autocomplete="off" spellcheck="false">
          <div class="help">In ${KIND_LABEL[kind]} under Settings, General.</div></div>`}
        <div class="check-field"><input type="checkbox" id="i-enabled" name="enabled" ${inst.enabled ? "checked" : ""}><label for="i-enabled">Enabled</label></div>
      </div>
      <div class="modal-foot">
        ${inst.id ? `<button class="btn ghost" type="button" data-act="remove-instance" style="color:var(--danger);margin-right:auto">Remove</button>` : ""}
        <span class="test-result" id="test-result" aria-live="polite"></span>
        <button class="btn" type="button" data-act="test-instance">Test</button>
        <button class="btn" type="button" data-act="close">Cancel</button>
        <button class="btn primary" type="submit">Save</button>
      </div>
    </form>`;
}

let editing = null; // { kind, index }

function openInstance(kind, index) {
  const list = kind === "qbit" ? state.config.qbittorrent : state.config[kind];
  const inst = index == null
    ? { name: "", url: "", apiKey: "", username: "", password: "", enabled: true, categories: [], pathMappings: [] }
    : list[index];
  editing = { kind, index };
  showModal(instanceForm(kind, inst));
  $("#i-name").focus();
}

function readInstance() {
  const f = $("#instance-form");
  const { kind, index } = editing;
  const list = kind === "qbit" ? state.config.qbittorrent : state.config[kind];
  const base = index == null ? {} : { ...list[index] };
  const inst = { ...base, name: f.name.value, url: f.url.value, enabled: f.enabled.checked };
  if (kind === "qbit") {
    inst.username = f.username.value;
    inst.password = f.password.value;
    inst.categories = f.categories.value.split(",").map((x) => x.trim()).filter(Boolean);
    inst.pathMappings = parseLines(f.pathMappings.value).map((l) => {
      const [remote, local] = l.split("=>").map((x) => (x || "").trim());
      return { remote, local };
    });
  } else {
    inst.apiKey = f.apiKey.value;
    inst.kind = kind;
  }
  return inst;
}

async function saveConfig(next, message) {
  try {
    state.config = await api("PUT", "/api/config", next);
    toast(message);
    return true;
  } catch (e) {
    toast(esc(e.message), "err");
    return false;
  }
}

async function saveInstance() {
  const inst = readInstance();
  const next = structuredClone(state.config);
  const key = editing.kind === "qbit" ? "qbittorrent" : editing.kind;
  if (editing.index == null) next[key].push(inst);
  else next[key][editing.index] = inst;
  if (await saveConfig(next, `Saved ${esc(inst.name)}.`)) { closeModal(); render(); }
}

async function removeInstance() {
  const key = editing.kind === "qbit" ? "qbittorrent" : editing.kind;
  const next = structuredClone(state.config);
  const [gone] = next[key].splice(editing.index, 1);
  if (await saveConfig(next, `Removed ${esc(gone.name)}.`)) { closeModal(); render(); }
}

async function testInstance() {
  const out = $("#test-result");
  out.className = "test-result";
  out.textContent = "Testing…";
  try {
    const r = await api("POST", editing.kind === "qbit" ? "/api/test/qbit" : "/api/test/arr", readInstance());
    out.className = "test-result ok";
    out.textContent = `Connected to ${r.message}`;
  } catch (e) {
    out.className = "test-result err";
    out.textContent = e.message;
  }
}

async function savePaths(form) {
  const next = structuredClone(state.config);
  for (const name of ["downloadPaths", "extraLibraryPaths", "excludedPaths", "ignoredNames"]) next[name] = parseLines(form[name].value);
  next.minAgeHours = Number(form.minAgeHours.value) || 0;
  next.scanIntervalHours = Number(form.scanIntervalHours.value) || 0;
  next.includeRecycleBins = form.includeRecycleBins.checked;
  if (await saveConfig(next, "Settings saved. They apply to the next scan.")) render();
}

/* ---------- system ---------- */

function renderSystem() {
  const s = state.status;
  const ls = s?.lastScan;
  const kindLabel = { library: "Library", recycle: "Recycle bin", download: "Downloads" };
  const stats = ls ? `
    <dl class="stats">
      <div><dt>Last scan</dt><dd title="${esc(fmtDate(ls.finishedAt))}">${esc(timeAgo(ls.finishedAt))}</dd></div>
      <div><dt>Took</dt><dd>${fmtDuration(ls.startedAt, ls.finishedAt)}</dd></div>
      <div><dt>Files read</dt><dd>${ls.filesScanned.toLocaleString()}</dd></div>
      <div><dt>Tracked by Sonarr and Radarr</dt><dd>${ls.trackedFiles.toLocaleString()}</dd></div>
      <div><dt>Torrents</dt><dd>${ls.torrents.toLocaleString()}</dd></div>
      <div><dt>Items found</dt><dd>${s.summary.items.toLocaleString()}</dd></div>
    </dl>` : `<div class="none-yet">No scan has run since Cleanarr started.</div>`;
  const roots = ls?.roots?.length ? `
    <div class="table-wrap" style="max-width:900px"><table>
      <thead><tr><th>Path</th><th>Used for</th><th>Disk</th></tr></thead>
      <tbody>${ls.roots.map((r) => `
        <tr><td><div class="name">${esc(r.path)}</div>${r.error ? `<div class="sub" style="color:var(--danger)">${esc(r.error)}</div>` : ""}</td>
        <td class="muted">${kindLabel[r.kind] || r.kind}</td>
        <td>${r.total ? `${fmtBytes(r.free)} free of ${fmtBytes(r.total)}<div class="mini-bar"><i style="width:${((r.total - r.free) / r.total) * 100}%"></i></div>` : ""}</td></tr>`).join("")}
      </tbody></table></div>` : "";
  const warnings = ls?.warnings?.length ? `<h2>Scan warnings</h2><ul class="warnings">${ls.warnings.map((w) => `<li>${esc(w)}</li>`).join("")}</ul>` : "";
  return `<div class="toolbar"><button class="tool" data-act="scan" ${s?.scan.running ? "disabled" : ""}>${icon("scan")}${s?.scan.running ? "Scanning" : "Scan now"}</button></div>
    <div class="page">
      <h1>System</h1>
      <p class="lede">Cleanarr ${esc(s?.version || "")}. ${s?.scan.running ? esc(s.scan.message) + "." : s?.scan.error ? `The last scan failed: ${esc(s.scan.error)}` : ""}</p>
      <h2>Scan</h2>${stats}
      ${roots ? `<h2>Scanned paths</h2>${roots}` : ""}
      ${warnings}
    </div>`;
}

/* ---------- events ---------- */

document.addEventListener("click", async (e) => {
  const act = e.target.closest("[data-act]");
  if (act) {
    const a = act.dataset.act;
    if (a === "backdrop" && e.target !== act) return; // clicks inside the modal
    switch (a) {
      case "scan": return startScan();
      case "select-all": for (const it of visibleItems()) state.selected.add(it.id); return updateSelection();
      case "clear": state.selected.clear(); return updateSelection();
      case "delete": return openDelete();
      case "confirm-delete": return confirmDelete(act);
      case "close": case "backdrop": return closeModal();
      case "add-instance": return openInstance(act.dataset.kind, null);
      case "edit-instance": return openInstance(act.dataset.kind, Number(act.dataset.index));
      case "test-instance": return testInstance();
      case "remove-instance": return removeInstance();
    }
  }
  const cat = e.target.closest(".cat");
  if (cat) {
    const id = cat.dataset.cat;
    state.hiddenCats.has(id) ? state.hiddenCats.delete(id) : state.hiddenCats.add(id);
    return render();
  }
  const th = e.target.closest("th[data-sort]");
  if (th) {
    const key = th.dataset.sort;
    state.sort = state.sort.key === key ? { key, dir: -state.sort.dir } : { key, dir: ["name", "related", "category"].includes(key) ? 1 : -1 };
    return render();
  }
  const jobRow = e.target.closest("tr[data-job]");
  if (jobRow) {
    const id = jobRow.dataset.job;
    state.expandedJob = state.expandedJob === id ? null : id;
    state.jobDetail = null;
    render();
    if (state.expandedJob) await refreshActivity();
    return;
  }
  const row = e.target.closest("tr.row[data-id]");
  if (row && !e.target.closest("a")) {
    const id = row.dataset.id;
    if (e.target.matches("input[data-sel]") || e.target.closest("td.check")) {
      state.selected.has(id) ? state.selected.delete(id) : state.selected.add(id);
      return updateSelection();
    }
    state.expanded.has(id) ? state.expanded.delete(id) : state.expanded.add(id);
    return render();
  }
});

document.addEventListener("change", (e) => {
  if (e.target.id === "check-all") {
    const shown = visibleItems();
    for (const it of shown) e.target.checked ? state.selected.add(it.id) : state.selected.delete(it.id);
    updateSelection();
  }
});

document.addEventListener("submit", (e) => {
  e.preventDefault();
  if (e.target.id === "instance-form") saveInstance();
  if (e.target.id === "paths-form") savePaths(e.target);
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && $("#modal-root").firstChild) closeModal();
});

let searchTimer;
$("#search").addEventListener("input", (e) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => { state.query = e.target.value; if (state.route === "cleanup") render(); }, 120);
});
$("#menu").addEventListener("click", () => $("#rail").classList.toggle("open"));
window.addEventListener("hashchange", route);

(async function init() {
  await Promise.all([loadStatus(), loadItems(), loadConfig()]);
  await route();
  schedulePoll();
})();
