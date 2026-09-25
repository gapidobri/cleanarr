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
  const watchDone = prev && prev.scan.watch && !s.scan.watch && !s.scan.running;
  if (scanDone) {
    if (s.scan.error) toast(`Scan failed: ${esc(s.scan.error)}`, "err");
    else toast(`Scan finished. ${esc(s.scan.message)}.`);
  }
  if (scanDone || jobsDone) await loadItems();
  if ((scanDone || watchDone) && space.loaded) await loadSpace().catch(() => {});
  if (jobsDone) {
    await loadJobs();
    toast(`Deletion finished. <a href="#/activity">See what was deleted</a>`);
  }
  if (state.route === "activity" && (s.activeJobs > 0 || jobsDone)) await refreshActivity();
  if (scanDone || jobsDone || (watchDone && state.route === "space") || (prev && prev.scan.running !== s.scan.running)) render();
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
  const busy = state.status && (state.status.scan.running || state.status.scan.watch || state.status.activeJobs > 0);
  setTimeout(async () => { await loadStatus(); schedulePoll(); }, busy ? 1500 : 5000);
}

function renderScanState() {
  const s = state.status;
  const el = $("#scan-state");
  if (!s) return;
  if (s.scan.running) {
    el.innerHTML = `<span class="spin"></span><span class="msg">${esc(s.scan.message)}</span>`;
  } else if (s.scan.watch) {
    el.innerHTML = `<span class="spin"></span><span class="msg">${esc(s.scan.watch)}</span>`;
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

const routes = { "": "cleanup", space: "space", activity: "activity", settings: "settings", system: "system" };

async function route() {
  const key = location.hash.replace(/^#\/?/, "").split("/")[0];
  state.route = routes[key] || "cleanup";
  for (const a of $$(".rail a")) {
    if (a.dataset.route === state.route) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  $("#rail").classList.remove("open");
  if ($("#modal-root").firstChild) closeModal();
  $(".search").style.visibility = state.route === "cleanup" || state.route === "space" ? "" : "hidden";
  if (state.route === "activity") await loadJobs();
  if (state.route === "space" && !space.loaded) {
    render();
    await loadSpace().catch((e) => toast(esc(e.message), "err"));
  }
  if (state.route === "settings" || !state.config) await loadConfig();
  render();
  window.scrollTo(0, 0);
}

function render() {
  const view = $("#view");
  const fn = { cleanup: renderCleanup, space: renderSpace, activity: renderActivity, settings: renderSettings, system: renderSystem }[state.route];
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

/* ---------- space ---------- */

// Validated against the dark surface in this order (adjacent segments stay
// apart for color-blind readers): instances first, then the fixed classes.
const ARR_COLORS = ["#3987e5", "#d95926", "#9085e9", "#e66767"];
const CLASS_COLORS = { cleanup: "#199e70", extras: "#c98500", torrents: "#d55181", other: "#7d8594" };
const SPACE_TABS = [["titles", "Titles"], ["folders", "Folders"], ["quality", "Quality"]];

const space = {
  data: null,
  loaded: false,
  tab: "titles",
  dir: null,
  path: "",
  arr: "", // class id filter on the Titles tab
  quality: "", // quality filter on the Titles tab
  unwatched: 0, // days; lists titles not watched for that long
  sort: { key: "size", dir: -1 },
  showAll: false,
};

async function loadSpace() {
  const data = await api("GET", "/api/space");
  space.loaded = true;
  if (!data.classes) { space.data = null; space.dir = null; return; }
  let n = 0;
  for (const c of data.classes) {
    c.color = c.id.startsWith("arr:") ? ARR_COLORS[n++] || CLASS_COLORS.other : CLASS_COLORS[c.kind];
  }
  space.data = data;
  await loadDir(space.path).catch(() => loadDir(""));
}

async function loadDir(path) {
  space.dir = await api("GET", `/api/space/dir?path=${encodeURIComponent(path)}`);
  space.path = path;
}

const pctOf = (n, of) => (of > 0 ? (n / of) * 100 : 0);
function fmtPct(p) {
  if (p <= 0) return "0%";
  if (p < 0.1) return "<0.1%";
  return p < 10 ? `${p.toFixed(1)}%` : `${Math.round(p)}%`;
}

const CLASS_NOTE = {
  cleanup: `Old versions, unused torrents, leftovers and the recycle bin. <a href="#/">Review on Cleanup</a>`,
  extras: "Subtitles, artwork, NFO files and recent videos in movie and series folders that are not tracked",
  torrents: "Seeding or downloading data that is not in the library, but is kept because of its category or progress",
  other: "Files in the scanned folders that belong to none of the above",
};

// segments renders the colored parts of a stacked bar. scale is the value
// that fills 100% of the bar.
function segments(parts, scale) {
  return space.data.classes.map((c, i) => {
    const v = parts[i] || 0;
    if (!v) return "";
    return `<i class="sseg" style="--c:${c.color};width:${pctOf(v, scale)}%" data-tip="${esc(c.label)}: ${fmtBytes(v)}"></i>`;
  }).join("");
}

function spaceVolumeHTML(v) {
  const used = v.total - v.free;
  const outside = Math.max(0, used - v.scanned);
  const names = v.paths.length === 1 ? `<b>${esc(v.paths[0])}</b>` : `<b>${esc(commonDir(v.paths))}</b>, ${v.paths.length} scanned folders`;
  return `
    <div class="volume">
      <div class="volume-head">
        <div class="volume-name" title="${esc(v.paths.join("\n"))}">${names}</div>
        <div class="volume-figures">
          <span>${fmtBytes(used)}<em>used of ${fmtBytes(v.total)}</em></span>
          <span>${fmtBytes(v.free)}<em>free</em></span>
        </div>
      </div>
      <div class="sbar" role="img" aria-label="${fmtBytes(used)} used: ${esc(space.data.classes.map((c, i) => v.parts[i] ? `${c.label} ${fmtBytes(v.parts[i])}` : "").filter(Boolean).join(", "))}${outside ? `, not in scanned folders ${fmtBytes(outside)}` : ""}. ${fmtBytes(v.free)} free.">
        ${segments(v.parts, v.total)}${outside ? `<i class="sseg outside" style="width:${pctOf(outside, v.total)}%" data-tip="Not in scanned folders: ${fmtBytes(outside)}"></i>` : ""}
      </div>
    </div>`;
}

function breakdownHTML() {
  const d = space.data;
  const outside = d.volumes.reduce((a, v) => a + Math.max(0, v.total - v.free - v.scanned), 0);
  const whole = d.size + outside;
  const titles = {};
  for (const t of d.titles) titles[t.class] = (titles[t.class] || 0) + 1;
  const rows = d.classes.filter((c) => c.size > 0).map((c) => {
    let note = CLASS_NOTE[c.kind] || "";
    if (c.id.startsWith("arr:")) {
      note = `${plural(titles[c.id] || 0, c.kind === "sonarr" ? "series" : "movie", c.kind === "sonarr" ? "series" : "movies")}, ${plural(c.files, "file")}`;
      if (c.seeding) note += `. ${fmtBytes(c.seeding)} of it is also seeding through hardlinks, which takes no extra space.`;
    }
    const filter = c.id.startsWith("arr:") ? ` data-act="space-arr" data-class="${esc(c.id)}"` : "";
    return `
      <div class="brow"${filter}>
        <span class="sw" style="--c:${c.color}"></span>
        <div class="blabel"><div class="name">${esc(c.label)}</div><div class="sub">${note}</div></div>
        <div class="bsize">${fmtBytes(c.size)}<em>${fmtPct(pctOf(c.size, whole))}</em></div>
      </div>`;
  });
  if (outside > 0) {
    rows.push(`
      <div class="brow">
        <span class="sw outside"></span>
        <div class="blabel"><div class="name">Not in scanned folders</div><div class="sub">Used on the same disks, but outside the folders Cleanarr scans: other shares, snapshots, #recycle, excluded paths and ignored names like @eaDir.</div></div>
        <div class="bsize">${fmtBytes(outside)}<em>${fmtPct(pctOf(outside, whole))}</em></div>
      </div>`);
  }
  return `<div class="breakdown">${rows.join("")}</div>`;
}

function renderSpace() {
  const s = state.status;
  const scanning = s?.scan.running;
  const toolbar = `<div class="toolbar"><button class="tool" data-act="scan" ${scanning || !hasArrs() ? "disabled" : ""}>${icon("scan")}${scanning ? "Scanning" : "Scan now"}</button></div>`;
  const d = space.data;
  if (!d) {
    const msg = !hasArrs()
      ? `<p>Connect Sonarr or Radarr first.</p><a class="btn primary" href="#/settings">Open settings</a>`
      : scanning ? `<p>${esc(s.scan.message)}.</p>`
      : `<p>A scan measures every file in the Sonarr and Radarr root folders and your download paths.</p><button class="btn primary" data-act="scan">Scan now</button>`;
    return toolbar + `<div class="page"><div class="empty"><h2>${scanning ? "Scanning your library" : space.loaded ? "No scan yet" : "Loading…"}</h2>${space.loaded ? msg : ""}</div></div>`;
  }
  const tabs = SPACE_TABS.map(([id, label]) => `<button role="tab" aria-selected="${space.tab === id}" data-act="space-tab" data-tab="${id}">${label}</button>`).join("");
  const body = { titles: titlesHTML, folders: foldersHTML, quality: qualityHTML }[space.tab]();
  return toolbar + `
    <div class="page">
      <h1>Space</h1>
      <p class="lede">What fills your disks, measured by the last scan${s?.lastScan ? ` ${esc(timeAgo(s.lastScan.finishedAt))}` : ""}. Deleting items does not update this page until the next scan. Data with several hardlinks, like a movie that is also seeding, is counted once, at the library copy.</p>
      ${d.volumes.length ? `<section class="volumes">${d.volumes.map(spaceVolumeHTML).join("")}</section>` : ""}
      ${breakdownHTML()}
      <div class="tabs" role="tablist">${tabs}</div>
      ${body}
    </div>`;
}

function spaceQuery() { return state.query.trim().toLowerCase(); }

function sortTh(key, label, cls = "") {
  const sorted = space.sort.key === key ? ` aria-sort="${space.sort.dir > 0 ? "ascending" : "descending"}"` : "";
  return `<th class="sortable ${cls}" data-space-sort="${key}"${sorted}>${label}</th>`;
}

const UNWATCHED = [[0, "Any time"], [90, "3 months"], [180, "6 months"], [365, "1 year"], [730, "2 years"]];
const DAY = 24 * 3600 * 1000;

// notWatchedSince reports whether nobody played t since cutoff (a time in
// ms). Titles added after cutoff or unknown to Jellyfin do not count.
function notWatchedSince(t, cutoff) {
  if (!t.watch) return false;
  if (t.added && new Date(t.added).getTime() > cutoff) return false;
  return !t.watch.last || new Date(t.watch.last).getTime() < cutoff;
}

function watchedHTML(t) {
  const added = t.added ? `<div class="sub" title="${esc(fmtDate(t.added))}">added ${esc(timeAgo(t.added))}</div>` : "";
  const w = t.watch;
  if (!w) return `<span class="faint" title="Jellyfin has no item in this folder">Not in Jellyfin</span>${added}`;
  if (w.last) {
    const tip = [
      w.lastUser ? `Last watched by ${w.lastUser} on ${fmtDate(w.last)}` : fmtDate(w.last),
      w.users.length ? `Watched by ${w.users.join(", ")}` : "",
    ].filter(Boolean).join("\n");
    return `<span title="${esc(tip)}">${esc(timeAgo(w.last))}</span>${added}`;
  }
  if (w.users.length) return `<span class="muted" title="Watched by ${esc(w.users.join(", "))}">Marked watched</span>${added}`;
  return `<span class="never">Never</span>${added}`;
}

function titlesHTML() {
  const d = space.data;
  const q = spaceQuery();
  const color = Object.fromEntries(d.classes.map((c) => [c.id, c.color]));
  const arrs = d.classes.filter((c) => c.id.startsWith("arr:") && c.size > 0);
  const cutoff = Date.now() - space.unwatched * DAY;
  let list = d.titles.filter((t) => (!space.arr || t.class === space.arr)
    && (!space.quality || t.qualities.some((x) => x.name === space.quality))
    && (!space.unwatched || notWatchedSince(t, cutoff))
    && (!q || [t.title, t.instance, t.path, ...t.qualities.map((x) => x.name)].some((v) => v && v.toLowerCase().includes(q))));
  const { key, dir } = space.sort;
  const val = (t) => ({ title: t.title.toLowerCase(), files: t.files, perFile: t.size / t.files, other: t.other, quality: (t.qualities[0]?.name || "").toLowerCase(),
    watched: t.watch ? (t.watch.last ? new Date(t.watch.last).getTime() : 0) : -1 })[key] ?? t.size;
  list.sort((a, b) => { const x = val(a), y = val(b); return x < y ? -dir : x > y ? dir : 0; });
  const total = list.reduce((a, t) => a + t.size, 0);
  const max = Math.max(...list.map((t) => t.size), 1);
  const shown = space.showAll ? list : list.slice(0, 100);

  const watchChips = d.watched ? `<div class="cats"><span class="cats-label">Not watched in</span>
    ${UNWATCHED.map(([days, label]) => `<button class="cat" data-act="space-unwatched" data-days="${days}" aria-pressed="${space.unwatched === days}">${label}</button>`).join("")}
  </div>` : d.watchLoading
    ? `<p class="tab-note">Loading watch history from Jellyfin. The Last watched column appears when it is ready.</p>`
    : state.config?.jellyfin?.some((j) => j.enabled)
    ? `<p class="tab-note warn-note">No watch history from Jellyfin. See the scan warnings on the <a href="#/system">System</a> page.</p>` : "";
  const chips = watchChips + (arrs.length > 1 || space.quality ? `<div class="cats">
    ${arrs.length > 1 ? `<button class="cat" data-act="space-arr" data-class="" aria-pressed="${!space.arr}">All</button>
    ${arrs.map((c) => `<button class="cat" data-act="space-arr" data-class="${esc(c.id)}" aria-pressed="${space.arr === c.id}" style="--cat:${c.color}"><span class="sw"></span>${esc(c.label)}</button>`).join("")}` : ""}
    ${space.quality ? `<button class="cat" data-act="space-quality" data-name="" aria-pressed="true" title="Show every quality">${esc(space.quality)}<span class="n">Remove</span></button>` : ""}
  </div>` : "");
  if (!list.length) return chips + `<div class="empty"><h2>No titles</h2><p>${q || space.arr || space.quality || space.unwatched ? "No titles match the filter." : "Sonarr and Radarr track no files in the scanned folders."}</p></div>`;
  const rows = shown.map((t) => {
    const qual = t.qualities.length ? `${esc(t.qualities[0].name)}${t.qualities.length > 1 ? ` <span class="faint" title="${esc(t.qualities.slice(1).map((x) => `${x.name}: ${fmtBytes(x.size)}`).join("\n"))}">+${t.qualities.length - 1}</span>` : ""}` : "";
    return `
      <tr class="row" data-act="space-open" data-path="${esc(t.path)}" tabindex="0">
        <td><div class="name">${esc(t.title)}</div><div class="sub">${esc(t.instance)}${t.seeding ? `, ${t.seeding >= t.size ? "seeding" : `${fmtBytes(t.seeding)} seeding`}` : ""}</div></td>
        <td class="col-kind">${qual}</td>
        <td class="num col-mod">${t.files.toLocaleString()}</td>
        <td class="num col-mod muted">${fmtBytes(t.size / t.files)}</td>
        <td class="num col-marks muted" title="Subtitles, extras and old versions in the folder">${t.other >= 1 << 20 ? fmtBytes(t.other) : ""}</td>
        ${d.watched ? `<td class="num watched">${watchedHTML(t)}</td>` : ""}
        <td class="num size-cell"><div class="sz"><span>${fmtBytes(t.size)}</span><div class="hbar"><i style="--c:${color[t.class]};width:${pctOf(t.size, max)}%"></i></div></div></td>
      </tr>`;
  }).join("");
  const more = list.length > shown.length
    ? `<div class="more"><button class="btn" data-act="space-all">Show all ${list.length.toLocaleString()} titles</button></div>` : "";
  return chips + `
    <p class="tab-note">${space.unwatched
      ? `<b>${plural(list.length, "title")}, ${fmtBytes(total)}</b>, not watched by anyone in ${UNWATCHED.find(([days]) => days === space.unwatched)[1]} and added before that.`
      : `${plural(list.length, "title")}, ${fmtBytes(total)}.`} Select a title to see its folder.</p>
    <div class="table-wrap"><table class="space-table">
      <thead><tr>
        ${sortTh("title", "Title")}
        ${sortTh("quality", "Quality", "col-kind")}
        ${sortTh("files", "Files", "num col-mod")}
        ${sortTh("perFile", "Per file", "num col-mod")}
        ${sortTh("other", "Other in folder", "num col-marks")}
        ${d.watched ? sortTh("watched", "Last watched", "num") : ""}
        ${sortTh("size", "Size", "num size-cell")}
      </tr></thead>
      <tbody>${rows}</tbody>
    </table></div>${more}`;
}

function crumbsHTML(dir) {
  const parts = [`<button class="crumb" data-act="space-dir" data-path="">All scanned folders</button>`];
  if (dir.path) {
    const top = dir.top || dir.path;
    parts.push(`<button class="crumb" data-act="space-dir" data-path="${esc(top)}">${esc(top)}</button>`);
    let p = top;
    for (const seg of dir.path.slice(top.length).split("/").filter(Boolean)) {
      p += "/" + seg;
      parts.push(`<button class="crumb" data-act="space-dir" data-path="${esc(p)}">${esc(seg)}</button>`);
    }
  }
  parts[parts.length - 1] = parts[parts.length - 1].replace('class="crumb"', 'class="crumb" aria-current="location"');
  return `<nav class="crumbs" aria-label="Folder">${parts.join('<span class="sep">/</span>')}</nav>`;
}

function foldersHTML() {
  const dir = space.dir;
  if (!dir) return `<p class="muted">Loading…</p>`;
  const q = spaceQuery();
  const list = dir.entries.filter((e) => !q || e.name.toLowerCase().includes(q) || (e.title || "").toLowerCase().includes(q));
  const max = Math.max(...dir.entries.map((e) => e.size), 1);
  const rows = list.map((e) => {
    const sub = [];
    if (e.title) sub.push(esc(e.title));
    if (e.isDir) sub.push(plural(e.files, "file"));
    if (e.linked) sub.push(e.size ? `plus ${fmtBytes(e.linked)} in hardlinks counted elsewhere` : `hardlink, counted at another path`);
    return `
      <tr class="row${e.isDir ? "" : " file"}"${e.isDir ? ` data-act="space-dir" data-path="${esc(e.path)}" tabindex="0"` : ""}>
        <td class="ico">${icon(e.isDir ? "folder" : "file")}</td>
        <td><div class="name">${esc(e.name)}</div>${sub.length ? `<div class="sub">${sub.join(", ")}</div>` : ""}</td>
        <td class="num col-mod muted">${fmtPct(pctOf(e.size, dir.size))}</td>
        <td class="num size-cell"><div class="sz"><span>${e.size || !e.linked ? fmtBytes(e.size) : `<span class="faint">${fmtBytes(e.linked)}</span>`}</span><div class="hbar stack">${segments(e.parts, max)}</div></div></td>
      </tr>`;
  }).join("");
  const more = dir.more ? `<tr><td></td><td class="muted" colspan="3">${plural(dir.more, "smaller entry", "smaller entries")} not shown, ${fmtBytes(dir.moreSize)}</td></tr>` : "";
  return `
    ${crumbsHTML(dir)}
    <p class="tab-note">${fmtBytes(dir.size)} in ${plural(dir.files, "file")}${dir.linked ? `, plus ${fmtBytes(dir.linked)} in hardlinks counted elsewhere` : ""}.</p>
    ${list.length ? `<div class="table-wrap"><table class="space-table">
      <thead><tr><th class="ico"></th><th>Name</th><th class="num col-mod">Share</th><th class="num size-cell">Size</th></tr></thead>
      <tbody>${rows}${more}</tbody>
    </table></div>` : `<div class="empty"><h2>${q ? "No matches" : "Empty"}</h2></div>`}`;
}

function qualityHTML() {
  const d = space.data;
  const q = spaceQuery();
  const list = d.qualities.filter((x) => !q || x.name.toLowerCase().includes(q));
  const total = d.qualities.reduce((a, x) => a + x.size, 0);
  const max = Math.max(...list.map((x) => x.size), 1);
  if (!list.length) return `<div class="empty"><h2>No matches</h2></div>`;
  return `
    <p class="tab-note">Quality as reported by Sonarr and Radarr, for the ${fmtBytes(total)} they track. Select a quality to list its titles.</p>
    <div class="table-wrap"><table class="space-table">
      <thead><tr><th>Quality</th><th class="num">Titles</th><th class="num col-mod">Files</th><th class="num col-mod">Per file</th><th class="num col-mod">Share</th><th class="num size-cell">Size</th></tr></thead>
      <tbody>${list.map((x) => `
        <tr class="row" data-act="space-quality" data-name="${esc(x.name)}" tabindex="0">
          <td><div class="name">${esc(x.name)}</div></td>
          <td class="num">${x.titles.toLocaleString()}</td>
          <td class="num col-mod">${x.files.toLocaleString()}</td>
          <td class="num col-mod muted">${fmtBytes(x.size / x.files)}</td>
          <td class="num col-mod muted">${fmtPct(pctOf(x.size, total))}</td>
          <td class="num size-cell"><div class="sz"><span>${fmtBytes(x.size)}</span><div class="hbar"><i style="--c:${ARR_COLORS[0]};width:${pctOf(x.size, max)}%"></i></div></div></td>
        </tr>`).join("")}
      </tbody>
    </table></div>`;
}

async function openSpaceDir(path) {
  try {
    await loadDir(path);
  } catch (e) {
    toast(esc(e.message), "err");
    return;
  }
  space.tab = "folders";
  state.query = "";
  $("#search").value = "";
  render();
  $(".tabs")?.scrollIntoView({ block: "nearest" });
}

// A small tooltip for chart segments; the full numbers are in the tables.
const tip = document.createElement("div");
tip.className = "tip";
tip.hidden = true;
document.body.append(tip);
document.addEventListener("mouseover", (e) => {
  const el = e.target.closest("[data-tip]");
  tip.hidden = !el;
  if (el) tip.textContent = el.dataset.tip;
});
document.addEventListener("mousemove", (e) => {
  if (tip.hidden) return;
  const x = Math.min(e.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
  tip.style.transform = `translate(${x}px, ${e.clientY + 16}px)`;
});

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

const KIND_LABEL = { sonarr: "Sonarr", radarr: "Radarr", qbit: "qBittorrent", jellyfin: "Jellyfin" };

function instanceList(kind, list) {
  const head = `<div class="section-head"><h2>${KIND_LABEL[kind]}</h2><button class="btn" data-act="add-instance" data-kind="${kind}">${icon("plus")}Add ${KIND_LABEL[kind]}</button></div>`;
  if (!list.length) {
    const hint = kind === "qbit"
      ? "Connect qBittorrent to find old versions that are still seeding and to remove torrents together with their files."
      : kind === "jellyfin"
        ? "Connect Jellyfin to see on the Space page when each movie and series was last watched, and which ones nobody watches."
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
const parseMappings = (s) => parseLines(s).map((l) => {
  const [remote, local] = l.split("=>").map((x) => (x || "").trim());
  return { remote, local };
});

function renderSettings() {
  const c = state.config;
  if (!c) return `<div class="page"><p class="muted">Loading…</p></div>`;
  return `<div class="page">
    <h1>Settings</h1>
    <p class="lede">Cleanarr sees the same paths as Sonarr and Radarr. If qBittorrent runs in a container with different paths, add a path mapping to it.</p>
    ${instanceList("sonarr", c.sonarr)}
    ${instanceList("radarr", c.radarr)}
    ${instanceList("qbit", c.qbittorrent)}
    ${instanceList("jellyfin", c.jellyfin)}

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
  const isJ = kind === "jellyfin";
  const maps = (inst.pathMappings || []).map((m) => `${m.remote} => ${m.local}`).join("\n");
  return `
    <form id="instance-form">
      <div class="modal-head"><h2>${inst.id ? "Edit" : "Add"} ${KIND_LABEL[kind]}</h2></div>
      <div class="modal-body">
        <div class="row-fields">
          <div class="field"><label for="i-name">Name</label><input type="text" id="i-name" name="name" required value="${esc(inst.name)}" placeholder="${isQ ? "qBittorrent" : KIND_LABEL[kind] + " 4K"}"></div>
          <div class="field"><label for="i-url">URL</label><input type="url" id="i-url" name="url" required value="${esc(inst.url)}" placeholder="http://192.168.1.10:${{ sonarr: 8989, radarr: 7878, jellyfin: 8096 }[kind] || 8080}"></div>
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
          <div class="help">${isJ ? "In Jellyfin under Dashboard, API Keys. Cleanarr only reads from Jellyfin." : `In ${KIND_LABEL[kind]} under Settings, General.`}</div></div>
        ${isJ ? `<div class="field"><label for="i-maps">Path mappings</label><textarea id="i-maps" name="pathMappings" spellcheck="false" placeholder="/movies => /data/media/movies">${esc(maps)}</textarea>
          <div class="help">Only needed when Jellyfin sees your library at different paths than Cleanarr. One mapping per line: Jellyfin path => Cleanarr path.</div></div>` : ""}`}
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
    inst.pathMappings = parseMappings(f.pathMappings.value);
  } else {
    inst.apiKey = f.apiKey.value;
    if (kind === "jellyfin") inst.pathMappings = parseMappings(f.pathMappings.value);
    else inst.kind = kind;
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
    const r = await api("POST", { qbit: "/api/test/qbit", jellyfin: "/api/test/jellyfin" }[editing.kind] || "/api/test/arr", readInstance());
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
      case "space-tab": space.tab = act.dataset.tab; return render();
      case "space-dir": return openSpaceDir(act.dataset.path);
      case "space-open": return act.dataset.path ? openSpaceDir(act.dataset.path) : undefined;
      case "space-all": space.showAll = true; return render();
      case "space-arr":
        if (e.target.closest("a")) return;
        space.arr = act.dataset.class;
        space.tab = "titles";
        return render();
      case "space-unwatched":
        space.unwatched = Number(act.dataset.days);
        space.showAll = false;
        if (space.unwatched) space.sort = { key: "size", dir: -1 };
        return render();
      case "space-quality":
        space.quality = act.dataset.name;
        space.tab = "titles";
        return render();
    }
  }
  const cat = e.target.closest(".cat");
  if (cat) {
    const id = cat.dataset.cat;
    state.hiddenCats.has(id) ? state.hiddenCats.delete(id) : state.hiddenCats.add(id);
    return render();
  }
  const sth = e.target.closest("th[data-space-sort]");
  if (sth) {
    const key = sth.dataset.spaceSort;
    space.sort = space.sort.key === key ? { key, dir: -space.sort.dir } : { key, dir: ["title", "quality", "watched"].includes(key) ? 1 : -1 };
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
  if (e.key === "Enter" && e.target.matches("tr[data-act][tabindex]")) e.target.click();
});

let searchTimer;
$("#search").addEventListener("input", (e) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => { state.query = e.target.value; if (state.route === "cleanup" || state.route === "space") render(); }, 120);
});
$("#menu").addEventListener("click", () => $("#rail").classList.toggle("open"));
window.addEventListener("hashchange", route);

(async function init() {
  await Promise.all([loadStatus(), loadItems(), loadConfig()]);
  await route();
  schedulePoll();
})();
