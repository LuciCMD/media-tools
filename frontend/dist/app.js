/* Media Tools - frontend controller.
 *
 * The backend owns all logic; this layer renders the declarative tool
 * registry it receives and forwards user intent back over the Wails bridge.
 * Nothing here is specific to one tool: a tool's page is built from its
 * declaration alone.
 */

/* Bounds (Power of Ten rule 2). */
const LIMITS = {
  toasts: 4,
  jobRows: 200, // rows kept in the DOM
  jobMemory: 600, // jobs remembered here; the backend evicts past 512
  batchFiles: 500,
  saveDelayMs: 350,
  drawDelayMs: 100,
  sliderSteps: 400, // wider ranges get a number field instead of a slider
  whenDepth: 12, // a chain of "when" conditions, one per option at most
};

// Download leads because it's what most people open the app for
const CATEGORIES = [
  { id: "download", label: "Download" },
  { id: "video", label: "Video" },
  { id: "audio", label: "Audio" },
  { id: "image", label: "Image" },
  { id: "subtitles", label: "Subtitles" },
];

const STATE_LABELS = {
  queued: { text: "Queued", tone: "muted" },
  running: { text: "Running", tone: "accent" },
  succeeded: { text: "Done", tone: "good" },
  failed: { text: "Failed", tone: "bad" },
  canceled: { text: "Canceled", tone: "muted" },
};

const PRESETS = [
  { value: "ultrafast", label: "Ultrafast" },
  { value: "veryfast", label: "Very Fast" },
  { value: "fast", label: "Fast" },
  { value: "medium", label: "Medium" },
  { value: "slow", label: "Slow" },
  { value: "slower", label: "Slower" },
  { value: "veryslow", label: "Very Slow" },
];

const state = {
  tools: [],
  env: null,
  settings: null,
  view: { kind: "tool", id: null },
  files: [],
  url: "",
  outputDir: "",
  options: {},
  showAdvanced: false,
  info: null,
  filter: "",
  lastInGroup: {}, // category -> the tool last used in it
  openInfo: new Set(), // info boxes left open, kept across redraws
  litStep: 0,
  goFolded: false, // the last step shown as a slim bar of icon buttons
  jobs: new Map(),
  batch: new Set(), // jobs submitted since the queue was last idle
  speechProgress: null, // { id, received, total } while a model downloads
};

/* ---------- Bridge ---------- */

function api() {
  return window.go && window.go.app && window.go.app.App;
}

async function call(name, ...args) {
  const bound = api();
  if (!bound || typeof bound[name] !== "function") {
    throw new Error(`the backend method ${name} is unavailable`);
  }
  return bound[name](...args);
}

function runtimeCall(name, ...args) {
  if (window.runtime && typeof window.runtime[name] === "function") {
    return window.runtime[name](...args);
  }
  return undefined;
}

function errorText(err) {
  return (err && err.message) || String(err);
}

/* ---------- Per-viewer UI memory ----------
 * The last tool used, overall and in each group, and whether the last step
 * is folded
 * Kept in browser storage because losing it costs nothing, while anything
 * that matters lives in the backend's settings file */

const UI_KEY = "media-tools.ui";

function loadUI() {
  try {
    const raw = JSON.parse(localStorage.getItem(UI_KEY) || "{}");
    const last = raw.lastInGroup && typeof raw.lastInGroup === "object" ? raw.lastInGroup : {};
    CATEGORIES.forEach((cat) => {
      if (typeof last[cat.id] === "string") {
        state.lastInGroup[cat.id] = last[cat.id];
      }
    });
    state.goFolded = raw.goFolded === true;
    return typeof raw.lastTool === "string" ? raw.lastTool : "";
  } catch (err) {
    return "";
  }
}

function saveUI() {
  try {
    localStorage.setItem(
      UI_KEY,
      JSON.stringify({
        lastInGroup: state.lastInGroup,
        lastTool: state.view.kind === "tool" ? state.view.id : "",
        goFolded: state.goFolded,
      })
    );
  } catch (err) {
    // Storage can be unavailable; the defaults are fine.
  }
}

/* ---------- Boot ---------- */

async function boot() {
  hydrateIcons(document);
  const lastTool = loadUI();
  try {
    const [tools, env, settings] = await Promise.all([
      call("GetTools"),
      call("GetEnvironment"),
      call("GetSettings"),
    ]);
    state.tools = tools || [];
    state.env = env;
    state.settings = settings;
    document.getElementById("app-version").textContent = env.version ? `Version ${env.version}` : "";

    const first = state.tools.find((t) => t.id === lastTool) || startingTool();
    if (first) {
      state.view = { kind: "tool", id: first.id };
      state.options = defaultsFor(first);
      state.lastInGroup[first.category] = first.id;
    }
    renderNav();
    // Rendered unconditionally so an empty registry still shows a page
    // rather than leaving the main area blank.
    renderPage();
    wireGlobalControls();
    subscribe();
    await refreshJobs();
    renderStatus();

    (env.warnings || []).forEach((w) => toast(w, "warn", 0));
  } catch (err) {
    setStatus("bad", `Startup failed: ${errorText(err)}`);
    toast(`Startup failed: ${errorText(err)}`, "bad", 0);
  }
}

function subscribe() {
  runtimeCall("EventsOn", "job:update", onJobUpdate);
  runtimeCall("EventsOn", "env:update", (env) => {
    state.env = env;
    toast("Your encoders changed since last time, so the list has been updated.", "warn");
    refreshSettings();
  });
  runtimeCall("EventsOn", "speech:progress", onSpeechProgress);
  runtimeCall("EventsOn", "speech:done", onSpeechDone);
  runtimeCall("OnFileDrop", onFileDrop, false);
}

async function refreshSettings() {
  try {
    state.settings = await call("GetSettings");
  } catch (err) {
    // The previous settings stay in use.
  }
  renderStatus();
  if (state.view.kind === "settings" || state.view.kind === "tool") {
    refreshPage();
  }
}

/* ---------- Sign band and tabs ----------
 * The band says where you are on its left and signs every other place to
 * its right, and the strip under it holds the current group's tools */

function renderNav() {
  const tool = currentTool();
  const place = tool ? tool.category : state.view.kind;

  const here = document.getElementById("here");
  here.innerHTML =
    `<span class="pict">${icon(place, place)}</span>` +
    `<span class="here-name">${escapeHTML(placeLabel(place))}</span>`;

  const nav = document.getElementById("nav");
  nav.innerHTML = "";
  CATEGORIES.forEach((cat) => {
    if (cat.id !== place && toolsIn(cat.id).length > 0) {
      nav.appendChild(destination(cat.label, cat.id, () => openGroup(cat.id), false));
    }
  });

  const foot = document.getElementById("nav-foot");
  foot.innerHTML = "";
  if (place !== "inspect") {
    foot.appendChild(destination("Inspect", "inspect", () => openView("inspect", "inspect"), true));
  }
  if (place !== "settings") {
    foot.appendChild(destination("Settings", "settings", () => openView("settings", "settings"), true));
  }
  fitBand();
  renderTabs();
}

/* fitBand shows the Inspect and Settings labels whenever the band has room
 * for them beside the current sign, and drops to their icons when it hasn't */
function fitBand() {
  const band = document.getElementById("band");
  band.classList.remove("compact");
  if (band.scrollWidth > band.clientWidth) {
    band.classList.add("compact");
  }
}

function placeLabel(place) {
  if (place === "inspect") {
    return "Inspect a File";
  }
  if (place === "settings") {
    return "Settings";
  }
  return categoryLabel(place);
}

function destination(label, iconName, onClick, extra) {
  const btn = document.createElement("button");
  btn.className = extra ? "dest extra" : "dest";
  btn.setAttribute("aria-label", label);
  if (extra) {
    btn.dataset.tip = label;
  }
  btn.innerHTML = `<span class="pict">${icon(iconName, iconName)}</span><span class="label">${escapeHTML(label)}</span>`;
  btn.addEventListener("click", onClick);
  return btn;
}

function renderTabs() {
  const tabs = document.getElementById("tabs");
  tabs.innerHTML = "";
  const tool = currentTool();
  tabs.hidden = !tool;
  if (!tool) {
    return;
  }
  toolsIn(tool.category).forEach((t) => {
    const btn = document.createElement("button");
    btn.className = "tab";
    if (t.id === tool.id) {
      btn.setAttribute("aria-current", "page");
    }
    btn.innerHTML = `${icon(t.icon, t.category)}<span>${escapeHTML(t.name)}</span>`;
    btn.addEventListener("click", () => selectTool(t.id));
    tabs.appendChild(btn);
  });
}

/* startingTool opens a first start on the first group's first tool, passing
 * over the download tools when yt-dlp wasn't found, since they can't run */
function startingTool() {
  const usable = CATEGORIES.filter((cat) => cat.id !== "download" || (state.env && state.env.ytdlpPath));
  for (const cat of usable) {
    const tools = toolsIn(cat.id);
    if (tools.length > 0) {
      return tools[0];
    }
  }
  return state.tools[0];
}

function toolsIn(category) {
  return state.tools.filter((t) => t.category === category);
}

/* openGroup goes back to the tool last used in that group */
function openGroup(category) {
  const tools = toolsIn(category);
  const last = tools.find((t) => t.id === state.lastInGroup[category]);
  const target = last || tools[0];
  if (target) {
    selectTool(target.id);
  }
}

/* ---------- Find a tool ---------- */

function matchesFilter(tool, filter) {
  if (!filter) {
    return true;
  }
  const hay = `${tool.name} ${tool.description} ${tool.category}`.toLowerCase();
  return filter.split(/\s+/).every((word) => hay.includes(word));
}

function findMatches() {
  const filter = state.filter.trim().toLowerCase();
  if (!filter) {
    return [];
  }
  const out = [];
  CATEGORIES.forEach((cat) => {
    toolsIn(cat.id).forEach((t) => {
      if (matchesFilter(t, filter)) {
        out.push(t);
      }
    });
  });
  return out;
}

/* showFinder lists the matching tools under the search field, using the
 * same popup as the dropdowns so arrows, Enter and Escape behave alike */
function showFinder() {
  const field = document.getElementById("tool-filter");
  const matches = findMatches();
  if (!state.filter.trim()) {
    closePopup();
    return;
  }
  const choices = matches.map((t) => ({
    value: t.id,
    label: t.name,
    hint: categoryLabel(t.category),
    pict: icon(t.icon, t.category),
  }));
  showPopup(field, choices, "", (id) => {
    clearFinder();
    selectTool(id);
  }, { keepFocus: true, className: "finder", empty: "No tool matches that." });
}

function clearFinder() {
  const field = document.getElementById("tool-filter");
  field.value = "";
  state.filter = "";
  closePopup();
  field.blur();
}

function categoryLabel(id) {
  const cat = CATEGORIES.find((c) => c.id === id);
  return cat ? cat.label : capitalise(id);
}

function openView(kind, id) {
  if (kind === "tool") {
    selectTool(id);
    return;
  }
  state.view = { kind, id };
  renderNav();
  renderPage();
}

function selectTool(id) {
  const tool = state.tools.find((t) => t.id === id);
  if (!tool) {
    return;
  }
  state.view = { kind: "tool", id };
  // Keep the chosen files when the new tool accepts them, so picking a clip
  // and then deciding to trim rather than convert costs nothing.
  state.files = state.files.filter((p) => accepts(tool, p));
  if (tool.input === "file") {
    state.files = state.files.slice(0, 1);
  }
  state.info = null;
  state.showAdvanced = false;
  state.options = defaultsFor(tool);
  state.lastInGroup[tool.category] = tool.id;
  saveUI();
  renderNav();
  renderPage();
}

function currentTool() {
  return state.view.kind === "tool" ? state.tools.find((t) => t.id === state.view.id) : null;
}

function defaultsFor(tool) {
  const saved = (state.settings && state.settings.toolDefaults) || {};
  const out = {};
  (tool.options || []).forEach((opt) => {
    const key = `${tool.id}.${opt.key}`;
    out[opt.key] = saved[key] !== undefined && opt.kind !== "file" ? saved[key] : opt.default;
  });
  return out;
}

/* optionShown applies an option's "when" condition, written "key=a|b":
 * the option shows only while that other option has one of those values,
 * and that option is shown itself, since a hidden one keeps its value */
function optionShown(opt, tool) {
  const options = (tool && tool.options) || [];
  let current = opt;
  for (let depth = 0; depth < LIMITS.whenDepth; depth++) {
    if (!current || !current.when) {
      return true;
    }
    const at = current.when.indexOf("=");
    const key = current.when.slice(0, at);
    if (!current.when.slice(at + 1).split("|").includes(state.options[key])) {
      return false;
    }
    current = options.find((o) => o.key === key);
  }
  return false;
}

/* shownOptions is what a run sends: hidden options fall back to their
 * defaults on the backend, so a leftover value can't get in the way. */
function shownOptions(tool) {
  const out = {};
  (tool.options || []).forEach((opt) => {
    if (optionShown(opt, tool)) {
      out[opt.key] = state.options[opt.key];
    }
  });
  return out;
}

/* requirementOf says what the tool needs before it can run as set up: its
 * own requirement, or one attached to a choice currently selected. */
function requirementOf(tool) {
  if (tool.requires) {
    return tool.requires;
  }
  const needing = (tool.options || []).find(
    (opt) => opt.requires && optionShown(opt, tool) && opt.requires[state.options[opt.key]]
  );
  return needing ? needing.requires[state.options[needing.key]] : "";
}

/* ---------- Page ---------- */

/* The page is a scrolling column with, on a tool, its last step as a footer
 * under it, so the footer never covers what scrolls */
function renderPage() {
  // Find a Tool lives in the band, so a redraw of the page leaves it open
  if (openPopup && document.getElementById("page").contains(openPopup.owner)) {
    closePopup();
  }
  const page = document.getElementById("page");
  page.innerHTML = "";

  const inner = document.createElement("div");
  inner.className = "page-inner";
  inner.id = "page-scroll";
  page.appendChild(inner);

  if (state.view.kind === "settings") {
    renderSettingsPage(inner);
    return;
  }
  if (state.view.kind === "inspect") {
    renderInspectPage(inner);
    return;
  }
  renderToolPage(page, inner);
}

/* refreshPage redraws the current view where it is, for changes that don't
 * come from moving to another page, such as a finished download. */
function refreshPage() {
  const before = document.getElementById("page-scroll");
  const top = before ? before.scrollTop : 0;
  renderPage();
  document.getElementById("page-scroll").scrollTop = top;
}

function pageHead(title, description) {
  const head = document.createElement("header");
  head.className = "page-head";
  head.innerHTML = `<h1>${escapeHTML(title)}</h1><p>${escapeHTML(description || "")}</p>`;
  return head;
}

function card(title, hint) {
  const el = document.createElement("section");
  el.className = "card";
  if (title) {
    const head = document.createElement("div");
    head.className = "card-head";
    head.innerHTML =
      `<div><h2>${escapeHTML(title)}</h2>` + (hint ? `<div class="hint">${escapeHTML(hint)}</div>` : "") + `</div>`;
    el.appendChild(head);
  }
  return el;
}

/* A tool page is a short chain of numbered steps
 * The step that needs the person next is lit in sign blue, and the lit sign
 * moves as they go */
function renderToolPage(page, inner) {
  const tool = currentTool();
  if (!tool) {
    inner.appendChild(pageHead("No Tools", "The tool list could not be loaded."));
    return;
  }

  inner.appendChild(pageHead(tool.name, tool.description));
  const missing = requirementCard(tool);
  if (missing) {
    inner.appendChild(missing);
  }

  const steps = [];
  if (tool.input === "url") {
    steps.push(step("input", "Paste a Link", "A video, a channel or a playlist", urlBody(tool)));
  } else {
    steps.push(step("input", tool.input === "file" ? "Choose a File" : "Choose Files", filesNote(tool), filesBody(tool)));
  }
  if ((tool.options || []).length > 0) {
    const title = tool.input === "url" ? "Choose How It's Saved" : "Choose How It's Made";
    steps.push(step("options", title, "Remembered for next time", optionsBody(tool)));
  }
  steps.push(step("go", tool.input === "url" ? "Save and Download" : "Save and Run", "", goBody(tool)));
  steps.forEach((el, i) => {
    el.querySelector(".step-num").textContent = String(i + 1);
    (el.dataset.step === "go" ? page : inner).appendChild(el);
  });
  makeFoldable(steps[steps.length - 1]);

  state.litStep = null;
  updateActionBar();
}

function step(key, title, note, body) {
  const el = document.createElement("section");
  el.className = key === "go" ? "step go" : "step";
  el.dataset.step = key;
  el.setAttribute("aria-label", title);
  const num = document.createElement("div");
  num.className = "step-num";
  num.setAttribute("aria-hidden", "true");
  const main = document.createElement("div");
  main.className = "step-main";
  const head = document.createElement("h2");
  head.innerHTML =
    `<span>${escapeHTML(title)}</span>` +
    `<span class="step-arrow" aria-hidden="true">${icon("arrow-right")}</span>` +
    `<span class="step-note">${escapeHTML(note)}</span>`;
  main.append(head, body);
  el.append(num, main);
  return el;
}

/* nextStep names the step that needs the person now: the input until there
 * is enough of it, then an option still waiting for its file, then the last
 * step
 * Once there's input, nothing is lit while a requirement blocks the run,
 * since the notice above says what's missing */
function nextStep(tool) {
  if (!hasEnoughInput(tool)) {
    return "input";
  }
  if (missingFileOption(tool)) {
    return "options";
  }
  if (requirementOf(tool) === "speech-model" && !speechReady()) {
    return "";
  }
  return "go";
}

function hasEnoughInput(tool) {
  return tool.input === "url" ? state.url.trim().length > 0 : state.files.length >= (tool.combine ? 2 : 1);
}

/* A file option only shows while its file is needed, as with Burn In
 * Subtitles set to Choose a File, so a shown one left empty blocks the run */
function missingFileOption(tool) {
  return (tool.options || []).find((opt) => opt.kind === "file" && optionShown(opt, tool) && !state.options[opt.key]) || null;
}

/* A tool that needs something first, such as a speech model, explains it
 * and links to where it's set up. */
function requirementCard(tool) {
  if (requirementOf(tool) !== "speech-model" || speechReady()) {
    return null;
  }
  if (!speechSupported()) {
    return notice(
      "Needs a Newer ffmpeg",
      "This ffmpeg wasn't built with speech recognition. Transcribing needs version 8.0 or later with whisper, such as the full build from gyan.dev."
    );
  }
  const open = document.createElement("button");
  open.className = "btn";
  open.innerHTML = `${icon("settings")}Open Settings`;
  open.addEventListener("click", () => openView("settings", "settings"));
  return notice(
    "Speech Model Needed",
    "Transcribing runs entirely on this computer, using a speech model you download once in Settings.",
    open
  );
}

function notice(title, text, action) {
  const el = document.createElement("div");
  el.className = "notice";
  el.setAttribute("role", "note");
  el.innerHTML = `<span class="pict">${icon("alert")}</span><div><b>${escapeHTML(title)}</b>${escapeHTML(text)}</div>`;
  if (action) {
    el.appendChild(action);
  }
  return el;
}

function speechReady() {
  return speechSupported() && Boolean(state.env.speech.active);
}

function speechSupported() {
  return Boolean(state.env && state.env.speech && state.env.speech.supported);
}

/* ---------- Input ---------- */

function filesNote(tool) {
  if (tool.combine) {
    return "Joined in the order listed";
  }
  return tool.input === "file" ? "" : "Each file becomes its own job";
}

function filesBody(tool) {
  const single = tool.input === "file";
  const el = document.createElement("div");

  const zone = document.createElement("button");
  zone.className = "dropzone";
  zone.innerHTML =
    `<span class="pict">${icon("upload")}</span><span><strong>${single ? "Choose a File" : "Choose Files"}</strong>` +
    `<span class="hint">Or drop ${single ? "one" : "them"} anywhere in the window. ` +
    `${escapeHTML(acceptSummary(tool))}</span></span>`;
  zone.addEventListener("click", () => pickFiles(tool));
  el.appendChild(zone);

  const list = document.createElement("div");
  list.id = "file-list";
  el.appendChild(list);
  renderFileList(list);
  return el;
}

function acceptSummary(tool) {
  const accept = tool.accept || [];
  if (accept.length === 0) {
    return "";
  }
  const shown = accept.slice(0, 6).map((e) => e.toUpperCase());
  return accept.length > 6 ? `${shown.join(", ")} and more.` : `${shown.join(", ")}.`;
}

function renderFileList(list) {
  list = list || document.getElementById("file-list");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  if (state.files.length === 0) {
    return;
  }

  const box = document.createElement("div");
  box.className = "filelist";
  state.files.forEach((path, index) => box.appendChild(fileRow(path, index)));
  list.appendChild(box);

  if (state.files.length > 1) {
    const summary = document.createElement("div");
    summary.className = "row spread file-summary";
    summary.innerHTML = `<span class="hint">${state.files.length} files</span>`;
    const clear = document.createElement("button");
    clear.className = "btn small";
    clear.textContent = "Clear All";
    clear.addEventListener("click", () => setFiles([]));
    summary.appendChild(clear);
    list.appendChild(summary);
  }
}

function fileRow(path, index) {
  const row = document.createElement("div");
  row.className = "file-row";

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = baseName(path);
  name.dataset.tip = path;

  const dir = document.createElement("span");
  dir.className = "dir";
  dir.textContent = dirName(path);

  const remove = document.createElement("button");
  remove.className = "icon-btn";
  remove.innerHTML = icon("close");
  remove.setAttribute("aria-label", `Remove ${baseName(path)}`);
  remove.addEventListener("click", () => {
    const next = state.files.slice();
    next.splice(index, 1);
    setFiles(next);
  });

  const tool = currentTool();
  if (tool && tool.combine) {
    const order = document.createElement("span");
    order.className = "order";
    order.textContent = String(index + 1);
    row.append(order, name, dir, moveButton("arrow-up", "Move Up", index, -1), moveButton("arrow-down", "Move Down", index, 1), remove);
    return row;
  }
  row.append(name, dir, remove);
  return row;
}

function moveButton(iconName, label, index, step) {
  const button = document.createElement("button");
  button.className = "icon-btn";
  button.innerHTML = icon(iconName);
  button.setAttribute("aria-label", label);
  button.dataset.tip = label;
  const target = index + step;
  button.disabled = target < 0 || target >= state.files.length;
  button.addEventListener("click", () => {
    const next = state.files.slice();
    [next[index], next[target]] = [next[target], next[index]];
    setFiles(next);
    // Keep the focus on the file that moved, so it can be moved again.
    const rows = document.querySelectorAll("#file-list .file-row");
    const again = rows[target] && rows[target].querySelector(`[aria-label="${label}"]`);
    if (again && !again.disabled) {
      again.focus();
    }
  });
  return button;
}

function setFiles(files) {
  state.files = files;
  renderFileList();
  updateActionBar();
}

function addFiles(tool, paths) {
  if (tool.input === "file") {
    setFiles(paths.slice(0, 1));
    return;
  }
  const seen = new Set(state.files);
  const merged = state.files.slice();
  for (let i = 0; i < paths.length && merged.length < LIMITS.batchFiles; i++) {
    if (!seen.has(paths[i])) {
      seen.add(paths[i]);
      merged.push(paths[i]);
    }
  }
  if (merged.length >= LIMITS.batchFiles) {
    toast(`A batch holds at most ${LIMITS.batchFiles} files, so the rest were left out.`, "warn");
  }
  setFiles(merged);
}

async function pickFiles(tool) {
  try {
    const paths = await call("SelectFiles", tool.id);
    if (paths && paths.length > 0) {
      addFiles(tool, paths);
    }
  } catch (err) {
    toast(errorText(err), "bad");
  }
}

function onFileDrop(x, y, paths) {
  document.body.classList.remove("dragging");
  const dropped = (paths || []).filter(Boolean);
  if (dropped.length === 0) {
    return;
  }
  if (state.view.kind === "inspect") {
    loadInfo(dropped[0]);
    return;
  }
  const tool = currentTool();
  if (!tool || tool.input === "url") {
    toast("Open a tool that takes files, then drop them there.", "warn");
    return;
  }
  const usable = dropped.filter((p) => accepts(tool, p));
  const skipped = dropped.length - usable.length;
  if (usable.length > 0) {
    addFiles(tool, usable);
  }
  if (skipped > 0) {
    const what = skipped === 1 ? "1 file isn't" : `${skipped} files aren't`;
    toast(`${what} a type ${tool.name} can open, so it was left out.`, "warn");
  }
}

function accepts(tool, path) {
  const accept = tool.accept || [];
  if (accept.length === 0) {
    return true;
  }
  const ext = extName(path);
  return accept.includes(ext);
}

function urlBody(tool) {
  const row = document.createElement("div");
  row.className = "paste-row";
  const input = document.createElement("input");
  input.className = "input big";
  input.id = "url-input";
  input.type = "url";
  input.placeholder = "https://";
  input.value = state.url;
  input.setAttribute("aria-label", "Link");
  input.spellcheck = false;
  input.addEventListener("input", (e) => {
    state.url = e.target.value;
    updateActionBar();
  });

  const paste = document.createElement("button");
  paste.className = "btn tall";
  paste.textContent = "Paste";
  paste.addEventListener("click", async () => {
    const pending = runtimeCall("ClipboardGetText");
    if (pending === undefined) {
      toast("The clipboard can't be read here. Paste into the field with Ctrl+V instead.", "warn");
      return;
    }
    try {
      const text = await pending;
      // The page may have moved to another tool while the clipboard answered
      if (currentTool() !== tool || !input.isConnected) {
        return;
      }
      const words = typeof text === "string" ? text.trim().split(/\s+/).filter(Boolean) : [];
      if (words.length === 0) {
        toast("There's no text on the clipboard to paste.", "warn");
      } else {
        // A link field can't show line breaks, so only the first link goes in
        state.url = words[0];
        input.value = state.url;
        updateActionBar();
        if (words.length > 1) {
          toast("The clipboard held more than one line, so only the first link was pasted.", "warn");
        }
      }
    } catch (err) {
      toast(`Couldn't read the clipboard: ${errorText(err)}`, "bad");
    }
    input.focus();
  });
  row.append(input, paste);
  return row;
}

/* ---------- Options ---------- */

function optionsBody(tool) {
  const el = document.createElement("div");
  const shown = (tool.options || []).filter((opt) => optionShown(opt, tool));
  const basic = shown.filter((o) => !o.advanced);
  const advanced = shown.filter((o) => o.advanced);

  const grid = document.createElement("div");
  grid.className = "grid";
  basic.forEach((opt) => grid.appendChild(optionField(tool, opt)));
  el.appendChild(grid);

  if (advanced.length > 0) {
    const toggle = document.createElement("button");
    toggle.className = "disclosure";
    toggle.setAttribute("aria-expanded", String(state.showAdvanced));
    toggle.innerHTML = `<span class="chev slot">${icon("chevron")}</span>More Options`;

    const body = document.createElement("div");
    body.className = "grid advanced";
    body.hidden = !state.showAdvanced;
    advanced.forEach((opt) => body.appendChild(optionField(tool, opt)));

    toggle.addEventListener("click", () => {
      state.showAdvanced = !state.showAdvanced;
      body.hidden = !state.showAdvanced;
      toggle.setAttribute("aria-expanded", String(state.showAdvanced));
      if (state.showAdvanced) {
        body.scrollIntoView({ block: "nearest", behavior: "smooth" });
      }
    });
    el.append(toggle, body);
  }
  return el;
}

function optionField(tool, opt) {
  const field = document.createElement("div");
  // A file path needs the room of a whole row.
  field.className = opt.kind === "file" ? "field wide" : "field";
  const infoKey = `${tool.id}.${opt.key}`;

  if (opt.kind === "bool") {
    const row = document.createElement("div");
    row.className = "toggle-row";
    row.appendChild(optionControl(tool, opt));
    field.appendChild(row);
    addInfo(row, field, opt.label, opt.help, infoKey);
    return field;
  }
  const head = fieldLabel(`opt-${opt.key}`, opt.label);
  field.append(head, optionControl(tool, opt));
  addInfo(head, field, opt.label, opt.help, infoKey);
  return field;
}

/* fieldLabel keeps the label's own text in its own element, so a control
 * labelled by it doesn't also read out the info button beside it */
function fieldLabel(id, text) {
  const head = document.createElement("div");
  head.className = "field-label";
  const label = document.createElement("span");
  label.id = id;
  label.textContent = text;
  head.appendChild(label);
  return head;
}

/* addInfo puts an info button after an entry's label that opens its
 * description in a box under the entry
 * Open boxes stay open across redraws until closed */
function addInfo(labelRow, field, label, text, key) {
  if (!text) {
    return;
  }
  // Each odd character becomes its own code, so "a-b" and "a.b" stay apart
  const id = `info-${key.replace(/[^A-Za-z0-9_]/g, (c) => `-${c.charCodeAt(0)}`)}`;
  const open = state.openInfo.has(key);

  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "info-btn";
  btn.innerHTML = icon("info");
  btn.setAttribute("aria-label", `About ${label}`);
  btn.setAttribute("aria-controls", id);
  btn.setAttribute("aria-expanded", String(open));

  const box = document.createElement("div");
  box.className = "info-box";
  box.id = id;
  box.textContent = text;
  box.hidden = !open;

  btn.addEventListener("click", () => {
    const show = box.hidden;
    box.hidden = !show;
    btn.setAttribute("aria-expanded", String(show));
    if (show) {
      state.openInfo.add(key);
      box.scrollIntoView({ block: "nearest", behavior: "smooth" });
    } else {
      state.openInfo.delete(key);
    }
  });
  labelRow.appendChild(btn);
  field.appendChild(box);
}

function optionControl(tool, opt) {
  const value = state.options[opt.key];
  const set = (v) => setOption(tool, opt.key, v);

  if (opt.kind === "select") {
    return makeSelect({ labelledBy: `opt-${opt.key}`, choices: opt.choices || [], value, onChange: set });
  }
  if (opt.kind === "bool") {
    return makeToggle(opt.label, value === "true", (on) => set(on ? "true" : "false"));
  }
  if (opt.kind === "file") {
    return makeFilePicker(tool, opt);
  }
  // A minimum of 0 is left out of the JSON, so it arrives undefined.
  const min = opt.min || 0;
  if (opt.kind === "number" && opt.max > min) {
    const steps = (opt.max - min) / (opt.step || 1);
    if (steps <= LIMITS.sliderSteps) {
      return makeSlider({
        labelledBy: `opt-${opt.key}`,
        min,
        max: opt.max,
        step: opt.step || 1,
        value,
        unit: opt.unit,
        onInput: set,
      });
    }
    return makeNumber({ labelledBy: `opt-${opt.key}`, min, max: opt.max, step: opt.step || 1, value, onInput: set });
  }
  if (opt.kind === "number") {
    return makeNumber({ labelledBy: `opt-${opt.key}`, value, onInput: set });
  }
  return makeText({ labelledBy: `opt-${opt.key}`, value, onInput: set });
}

function setOption(tool, key, value) {
  state.options[key] = value;
  const opt = (tool.options || []).find((o) => o.key === key);
  if (opt && opt.kind !== "file") {
    state.settings.toolDefaults = state.settings.toolDefaults || {};
    state.settings.toolDefaults[`${tool.id}.${key}`] = value;
    scheduleSave();
  }
  // Options that show only for some values of this one appear or go.
  const prefix = `${key}=`;
  if ((tool.options || []).some((o) => o.when && o.when.startsWith(prefix))) {
    refreshPage();
    const again = document.querySelector(`button[aria-labelledby~="opt-${key}"]`);
    if (again) {
      again.focus();
    }
  } else {
    updateActionBar();
  }
}

/* makeFilePicker shows the chosen file in a filled field, with buttons to
 * choose one and to clear it. The picker opens beside the file being
 * worked on. */
function makeFilePicker(tool, opt) {
  const value = state.options[opt.key] || "";
  const row = document.createElement("div");
  row.className = "file-pick";

  const name = document.createElement("span");
  name.className = value ? "file-pick-name" : "file-pick-name empty";
  name.setAttribute("aria-labelledby", `opt-${opt.key}`);
  name.textContent = value ? baseName(value) : "No file chosen";
  if (value) {
    name.dataset.tip = value;
  }

  const choose = document.createElement("button");
  choose.className = "btn";
  choose.innerHTML = `${icon("folder")}${value ? "Change" : "Choose File"}`;
  choose.setAttribute("aria-label", `Choose ${opt.label}`);
  choose.addEventListener("click", async () => {
    try {
      const path = await call("SelectOptionFile", tool.id, opt.key, state.files[0] || "");
      if (path) {
        setOption(tool, opt.key, path);
        refreshPage();
      }
    } catch (err) {
      toast(errorText(err), "bad");
    }
  });
  row.append(name, choose);

  if (value) {
    const clear = document.createElement("button");
    clear.className = "icon-btn";
    clear.innerHTML = icon("close");
    clear.setAttribute("aria-label", `Clear ${opt.label}`);
    clear.dataset.tip = "Clear";
    clear.addEventListener("click", () => {
      setOption(tool, opt.key, "");
      refreshPage();
    });
    row.appendChild(clear);
  }
  return row;
}

/* ---------- Last step: where it goes, and Run ---------- */

function goBody(tool) {
  const el = document.createElement("div");
  el.className = "go-body";
  const where = document.createElement("div");
  where.className = "where";
  where.id = "dest-row";
  renderDestination(where, tool);

  const row = document.createElement("div");
  row.className = "go-row";
  row.innerHTML = `<span class="summary" id="run-summary"></span>`;

  if (tool.input !== "url") {
    const inspect = document.createElement("button");
    inspect.className = "btn";
    inspect.id = "btn-inspect";
    inspect.textContent = "Inspect";
    inspect.addEventListener("click", () => {
      const file = state.files[0];
      openView("inspect", "inspect");
      loadInfo(file);
    });
    row.appendChild(inspect);
  }

  const run = document.createElement("button");
  run.className = "btn primary";
  run.id = "btn-run";
  run.innerHTML = `<span class="pict">${icon(tool.input === "url" ? "download" : "arrow-right")}</span><span id="run-label">Run</span>`;
  run.addEventListener("click", () => submit(tool));
  row.appendChild(run);

  el.append(where, row);
  return el;
}

function renderDestination(row, tool) {
  row.innerHTML = `<span class="label">Save To</span>`;

  const path = document.createElement("span");
  const folder = state.outputDir || (tool.input === "url" ? "" : fixedFolder());
  if (folder) {
    path.className = "path";
    path.textContent = folder;
    path.dataset.tip = folder;
  } else if (tool.input === "url") {
    path.className = "path unset";
    path.textContent = "Choose a folder to download into";
  } else {
    path.className = "path";
    path.textContent = "Next to each original file";
  }

  const pick = document.createElement("button");
  pick.className = "btn pick";
  pick.classList.toggle("needs", tool.input === "url" && !folder);
  pick.innerHTML = `${icon("folder")}<span class="btn-text">Choose Folder</span>`;
  pick.setAttribute("aria-label", "Choose Folder");
  // Only shown folded, when the path beside it is hidden
  pick.dataset.tip = folder ? `Saving to ${folder}` : "Choose Folder";
  pick.dataset.tipWhen = "folded";
  pick.addEventListener("click", async () => {
    try {
      const dir = await call("SelectFolder", `${tool.id}.out`);
      if (dir) {
        state.outputDir = dir;
        renderDestination(row, tool);
        updateActionBar();
      }
    } catch (err) {
      toast(errorText(err), "bad");
    }
  });
  row.append(path, pick);

  if (state.outputDir && tool.input !== "url") {
    const reset = document.createElement("button");
    reset.className = "btn reset";
    reset.textContent = "Use Default";
    reset.addEventListener("click", () => {
      state.outputDir = "";
      renderDestination(row, tool);
      updateActionBar();
    });
    row.appendChild(reset);
  }
}

function fixedFolder() {
  return state.settings && state.settings.outputMode === "fixed" && state.settings.outputDir
    ? state.settings.outputDir
    : "";
}

/* ---------- Run state ---------- */

function updateActionBar() {
  const tool = currentTool();
  const run = document.getElementById("btn-run");
  if (!tool || !run) {
    return;
  }
  run.disabled = !canRun(tool);
  const label = runLabel(tool);
  document.getElementById("run-label").textContent = label;
  // Folded, the button is only its arrow, so the name moves to the tooltip
  run.setAttribute("aria-label", label);
  run.dataset.tip = state.goFolded ? `${label} (Ctrl+Enter)` : "Ctrl+Enter";

  const inspect = document.getElementById("btn-inspect");
  if (inspect) {
    inspect.classList.toggle("hidden", state.files.length !== 1);
  }
  document.getElementById("run-summary").innerHTML = runSummary(tool);
  signNextStep(tool);
}

/* signNextStep lights the step that needs the person now, and flips its
 * number in when the lit step moves */
function signNextStep(tool) {
  const next = nextStep(tool);
  document.querySelectorAll(".step").forEach((el) => {
    el.classList.toggle("lit", el.dataset.step === next);
  });
  const goNote = document.querySelector('.step[data-step="go"] .step-note');
  if (goNote) {
    goNote.textContent = goNoteText(tool);
  }
  const optionsNote = document.querySelector('.step[data-step="options"] .step-note');
  if (optionsNote) {
    const missing = missingFileOption(tool);
    optionsNote.textContent = missing ? `Choose a file for ${missing.label}` : "Remembered for next time";
  }
  if (next === state.litStep) {
    return;
  }
  // null means the page was just drawn, so its first light needs no flip
  const changed = state.litStep !== null;
  state.litStep = next;
  const num = next && document.querySelector(`.step[data-step="${next}"] .step-num`);
  if (num && changed) {
    num.classList.remove("flip");
    void num.offsetWidth; // restarts the animation
    num.classList.add("flip");
  }
}

/* The last step folds into a slim bar holding Choose Folder and Run as icons,
 * remembered for every tool, so the page keeps more of its height */
function makeFoldable(el) {
  const fold = foldButton("fold", "Fold", "Fold the last step");
  el.querySelector("h2").appendChild(fold);
  const unfold = foldButton("unfold", "Unfold", "Show the whole last step");
  el.querySelector(".step-main").prepend(unfold);
  fold.addEventListener("click", () => setFolded(el, true, unfold));
  unfold.addEventListener("click", () => setFolded(el, false, fold));
  el.classList.toggle("folded", state.goFolded);
}

function foldButton(kind, tip, label) {
  const btn = document.createElement("button");
  btn.className = `icon-btn ${kind}`;
  btn.innerHTML = icon("chevron");
  btn.dataset.tip = tip;
  btn.setAttribute("aria-label", label);
  return btn;
}

function setFolded(el, folded, focusNext) {
  state.goFolded = folded;
  el.classList.toggle("folded", folded);
  saveUI();
  updateActionBar();
  focusNext.focus();
}

function goNoteText(tool) {
  if (tool.input === "url" && state.url.trim() && !state.outputDir) {
    return "Choose a folder first";
  }
  return canRun(tool) ? "Ready" : "";
}

function runLabel(tool) {
  if (tool.input === "url") {
    return "Download";
  }
  if (tool.combine && state.files.length > 1) {
    return `Join ${state.files.length} Files`;
  }
  if (state.files.length > 1) {
    return `Run on ${state.files.length} Files`;
  }
  return "Run";
}

function runSummary(tool) {
  const parts = [];
  if (tool.input === "url") {
    if (!state.url.trim()) {
      parts.push("Paste a link to start.");
    } else if (!state.outputDir) {
      parts.push("Choose where to save it.");
    } else {
      parts.push(`Into <b>${escapeHTML(baseName(state.outputDir) || state.outputDir)}</b>`);
    }
  } else if (state.files.length === 0) {
    parts.push("Add a file to start.");
  } else {
    parts.push(state.files.length === 1 ? `<b>${escapeHTML(baseName(state.files[0]))}</b>` : `<b>${state.files.length} files</b>`);
    if (tool.combine && state.files.length === 1) {
      parts.push("Add another file to join them.");
    }
    if (requirementOf(tool) === "speech-model" && !speechReady()) {
      parts.push(speechSupported() ? "Download a speech model in Settings first." : "This ffmpeg can't transcribe.");
    }
  }
  const missing = missingFileOption(tool);
  if (missing) {
    parts.push(`Choose a file for ${escapeHTML(missing.label)} first.`);
  }
  if (tool.usesEncode && (state.files.length > 0 || tool.input === "url")) {
    const backend = activeBackend();
    if (backend) {
      // A size target sets the bitrate itself, so the quality doesn't apply.
      const quality = tool.needsDuration ? "" : ` at quality ${state.settings.quality}`;
      parts.push(`${escapeHTML(backend.label)}${quality}`);
    }
  }
  return parts.join(" &middot; ");
}

function canRun(tool) {
  if (missingFileOption(tool)) {
    return false;
  }
  if (tool.input === "url") {
    return state.url.trim().length > 0 && state.outputDir.trim().length > 0;
  }
  if (requirementOf(tool) === "speech-model" && !speechReady()) {
    return false;
  }
  return state.files.length >= (tool.combine ? 2 : 1);
}

async function submit(tool) {
  if (!tool || !canRun(tool)) {
    return;
  }
  const run = document.getElementById("btn-run");
  if (run) {
    run.disabled = true;
  }
  try {
    await flushSave();
    const ids = await call("Run", {
      toolId: tool.id,
      paths: state.files,
      url: state.url,
      options: shownOptions(tool),
      outputDir: state.outputDir,
    });
    const count = (ids || []).length;
    setStatus("good", count === 1 ? "Added 1 job." : `Added ${count} jobs.`);
  } catch (err) {
    toast(errorText(err), "bad", 9000);
  } finally {
    updateActionBar();
  }
}

/* ---------- Queue ---------- */

const jobRows = new Map(); // job id -> row element
const dirtyJobs = new Set();
let queueFrame = 0;

function onJobUpdate(job) {
  const known = state.jobs.has(job.id);
  // A download's later steps report no fraction, so the batch bar and the
  // window title keep the last one known rather than dropping to nothing
  const before = state.jobs.get(job.id);
  const fraction = job.progress ? job.progress.fraction : -1;
  job.lastFraction = fraction >= 0 ? fraction : (before && before.lastFraction) || 0;
  if (!known && activeJobCount() === 0) {
    // The queue was idle, so this job starts a new batch.
    state.batch.clear();
  }
  state.jobs.set(job.id, job);
  state.batch.add(job.id);
  if (!known) {
    forgetOldJobs();
  }
  dirtyJobs.add(job.id);
  if (!queueFrame) {
    // Progress arrives up to eight times a second per worker, so updates are
    // gathered and drawn together. A timer rather than requestAnimationFrame,
    // which stops while the window is minimised: exactly when the progress in
    // the window title is the thing being watched.
    queueFrame = setTimeout(flushQueue, LIMITS.drawDelayMs);
  }
}

function forgetOldJobs() {
  if (state.jobs.size <= LIMITS.jobMemory) {
    return;
  }
  for (const [id, job] of state.jobs) {
    if (state.jobs.size <= LIMITS.jobMemory) {
      break;
    }
    if (job.state !== "queued" && job.state !== "running") {
      state.jobs.delete(id);
      state.batch.delete(id);
      const row = jobRows.get(id);
      if (row) {
        row.remove();
        jobRows.delete(id);
      }
    }
  }
}

function flushQueue() {
  queueFrame = 0;
  const body = document.getElementById("queue-body");
  const empty = body.querySelector(".board-empty");
  if (empty && state.jobs.size > 0) {
    empty.remove();
  }

  dirtyJobs.forEach((id) => {
    const job = state.jobs.get(id);
    if (!job) {
      return;
    }
    const existing = jobRows.get(id);
    if (existing) {
      fillJobRow(existing, job);
      return;
    }
    const row = document.createElement("div");
    row.className = "job";
    fillJobRow(row, job);
    body.insertBefore(row, body.firstChild);
    jobRows.set(id, row);
  });
  dirtyJobs.clear();

  trimJobRows(body);
  renderQueueHead();
  renderStatus();
}

function trimJobRows(body) {
  while (body.children.length > LIMITS.jobRows) {
    const last = body.lastElementChild;
    for (const [id, row] of jobRows) {
      if (row === last) {
        jobRows.delete(id);
        break;
      }
    }
    last.remove();
  }
}

function rebuildQueue() {
  const body = document.getElementById("queue-body");
  body.innerHTML = "";
  jobRows.clear();
  const jobs = [...state.jobs.values()].reverse().slice(0, LIMITS.jobRows);
  if (jobs.length === 0) {
    body.innerHTML =
      `<div class="board-empty"><span class="pict">${icon("arrow-right")}</span>` +
      `<div><b>No Jobs Yet</b>They show up here as soon as you start one.</div></div>`;
  }
  jobs.forEach((job) => {
    const row = document.createElement("div");
    row.className = "job";
    fillJobRow(row, job);
    body.appendChild(row);
    jobRows.set(job.id, row);
  });
  renderQueueHead();
  renderStatus();
}

async function refreshJobs() {
  try {
    const list = await call("ListJobs");
    state.jobs = new Map((list || []).map((j) => [j.id, j]));
    state.batch = new Set([...state.jobs.values()].filter(isActive).map((j) => j.id));
  } catch (err) {
    // A queue that is not up yet is not worth interrupting anyone for.
  }
  rebuildQueue();
}

/* A row on the board: the big readout says how long is left, or the state
 * in a word, and the lines beside it give the real numbers behind it */
function fillJobRow(row, job) {
  const was = row.dataset.state;
  row.innerHTML = "";
  row.dataset.state = job.state;

  const big = jobReadout(job);
  if (was && was !== job.state) {
    big.classList.add("flip");
  }

  const main = document.createElement("div");
  main.className = "job-main";
  main.appendChild(jobHeader(job));
  main.appendChild(jobBar(job));
  const stats = jobStats(job);
  if (stats) {
    main.appendChild(stats);
  }
  if (job.state === "failed" && job.error) {
    const err = document.createElement("div");
    err.className = "job-error";
    err.textContent = job.error;
    main.appendChild(err);
  }
  const actions = jobActions(job);
  if (actions) {
    main.appendChild(actions);
  }
  row.append(big, main);
}

function jobReadout(job) {
  const el = document.createElement("div");
  el.className = "readout";
  const p = job.progress || {};
  const label = STATE_LABELS[job.state] || { text: capitalise(job.state), tone: "muted" };
  let value = label.text;
  let small = "";

  if (job.state === "running" && p.etaSeconds >= 0) {
    value = clock(p.etaSeconds);
    small = "left";
  } else if (job.state === "running" && p.fraction >= 0) {
    value = `${Math.floor(p.fraction * 100)}%`;
    small = "done";
  } else if (job.state === "running") {
    value = "Working";
    // A download's later steps report no progress, only which step it's on
    small = p.stage ? p.stage.toLowerCase() : "time unknown";
  } else if (job.state === "queued") {
    small = "waiting";
  }

  if (!/^\d/.test(value)) {
    el.classList.add("word");
  }
  if (label.tone === "good" || label.tone === "bad" || label.tone === "muted") {
    el.classList.add(label.tone);
  }
  el.innerHTML = `${escapeHTML(value)}` + (small ? `<small>${escapeHTML(small)}</small>` : "");
  return el;
}

/* The backend labels a file job "Tool - file" and a download with the tool
 * alone, so the file or the link leads here and the tool goes under it */
function jobHeader(job) {
  const top = document.createElement("div");
  const tool = state.tools.find((t) => t.id === job.tool);
  const prefix = tool ? `${tool.name} - ` : "";
  let label = prefix && job.label.startsWith(prefix) ? job.label.slice(prefix.length) : job.label;
  if (tool && label === tool.name && job.input) {
    label = job.input;
  }

  const name = document.createElement("div");
  name.className = "job-name";
  name.textContent = label;
  name.dataset.tip = job.input || job.label;
  top.appendChild(name);

  if (tool) {
    const what = document.createElement("div");
    what.className = "job-tool";
    what.textContent = tool.name;
    top.appendChild(what);
  }
  return top;
}

function jobActions(job) {
  const row = document.createElement("div");
  row.className = "job-actions";
  if (isActive(job)) {
    row.appendChild(jobButton("Cancel", job, () => call("CancelJob", job.id)));
  }
  if (job.state === "succeeded" && job.output) {
    row.appendChild(jobButton("Show in Folder", job, () => call("RevealOutput", job.output)));
  }
  if (job.state === "failed" || job.state === "canceled") {
    row.appendChild(jobButton("Retry", job, () => call("RetryJob", job.id)));
  }
  return row.children.length > 0 ? row : null;
}

function jobButton(text, job, action) {
  const btn = document.createElement("button");
  btn.className = "btn small";
  btn.textContent = text;
  btn.setAttribute("aria-label", `${text} ${job.label}`);
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    try {
      await action();
    } catch (err) {
      toast(errorText(err), "bad");
      btn.disabled = false;
    }
  });
  return btn;
}

function jobBar(job) {
  const bar = document.createElement("div");
  bar.className = "bar";
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-label", job.label);

  const fill = document.createElement("div");
  fill.className = "bar-fill";
  const fraction = job.progress ? job.progress.fraction : -1;

  if (job.state === "succeeded") {
    fill.classList.add("good");
    fill.style.width = "100%";
  } else if (job.state === "failed") {
    fill.classList.add("bad");
    fill.style.width = "100%";
  } else if (job.state === "canceled") {
    fill.classList.add("muted");
    fill.style.width = fraction > 0 ? `${(fraction * 100).toFixed(1)}%` : "0%";
  } else if (job.state === "running" && fraction < 0) {
    // No duration was derivable, so show live motion rather than a false value.
    bar.classList.add("indeterminate");
  } else if (fraction >= 0) {
    fill.style.width = `${Math.min(100, fraction * 100).toFixed(1)}%`;
    bar.setAttribute("aria-valuenow", String(Math.round(fraction * 100)));
  }
  bar.appendChild(fill);
  return bar;
}

function jobStats(job) {
  const p = job.progress;
  if (job.state === "queued") {
    return null;
  }
  const parts = [];
  if (p && job.state === "running") {
    // The big readout already shows the time left, or the percentage when
    // there's no time, so each number appears once
    if (p.fraction >= 0 && p.etaSeconds >= 0) {
      parts.push(`<b>${(p.fraction * 100).toFixed(1)}%</b>`);
    }
    if (p.stage && p.fraction >= 0) {
      parts.push(escapeHTML(p.stage));
    }
    if (p.items > 1 && p.item > 0) {
      parts.push(`item ${p.item} of ${p.items}`);
    }
    if (p.totalBytes > 0 && p.fraction >= 0) {
      parts.push(`${bytes(p.sizeBytes)} of ${bytes(p.totalBytes)}`);
    }
    if (p.bytesPerSec > 0) {
      parts.push(`${bytes(p.bytesPerSec)}/s`);
    }
    if (p.passes > 1) {
      parts.push(`pass ${p.pass} of ${p.passes}`);
    }
    if (p.totalSec > 0 && p.outTimeSec > 0) {
      parts.push(`${clock(p.outTimeSec)} of ${clock(p.totalSec)}`);
    }
    if (p.totalFrames > 0 && p.frame > 0) {
      parts.push(`frame ${p.frame.toLocaleString()} of ${p.totalFrames.toLocaleString()}`);
    } else if (p.frame > 0) {
      parts.push(`frame ${p.frame.toLocaleString()}`);
    }
    if (p.fps > 0) {
      parts.push(`${p.fps.toFixed(0)} fps`);
    }
    if (p.speed > 0) {
      parts.push(`${p.speed.toFixed(2)}x`);
    }
  }
  // A download's size already shows as "of" its total while it runs
  if (p && p.sizeBytes > 0 && !(job.state === "running" && p.totalBytes > 0 && p.fraction >= 0)) {
    parts.push(bytes(p.sizeBytes));
  }
  if (job.state !== "running" && job.startedAt && job.endedAt) {
    parts.push(`Took <b>${clock((job.endedAt - job.startedAt) / 1000)}</b>`);
  }
  // ffmpeg's speed is an average over the whole run, so a finished job's
  // last reading is its true overall speed
  if (job.state === "succeeded" && p && p.speed > 0) {
    parts.push(`${p.speed.toFixed(2)}x realtime`);
  }
  if (parts.length === 0) {
    return null;
  }
  const el = document.createElement("div");
  el.className = "job-stats";
  el.innerHTML = parts.map((part) => `<span>${part}</span>`).join("");
  return el;
}

function isActive(job) {
  return job.state === "queued" || job.state === "running";
}

function activeJobCount() {
  let n = 0;
  state.jobs.forEach((job) => {
    if (isActive(job)) {
      n += 1;
    }
  });
  return n;
}

/* batchProgress summarises the jobs submitted since the queue was last idle. */
function batchProgress() {
  const out = { total: 0, done: 0, failed: 0, running: 0, queued: 0, fraction: 0, known: true };
  let sum = 0;
  state.batch.forEach((id) => {
    const job = state.jobs.get(id);
    if (!job) {
      return;
    }
    out.total += 1;
    if (job.state === "running") {
      out.running += 1;
      const f = job.progress ? job.progress.fraction : -1;
      if (f < 0) {
        out.known = false;
      }
      sum += f >= 0 ? f : job.lastFraction || 0;
    } else if (job.state === "queued") {
      out.queued += 1;
    } else {
      out.done += 1;
      sum += 1;
      if (job.state === "failed") {
        out.failed += 1;
      }
    }
  });
  out.fraction = out.total > 0 ? sum / out.total : 0;
  return out;
}

function renderQueueHead() {
  const overall = document.getElementById("queue-overall");
  const b = batchProgress();
  const active = b.running + b.queued;
  overall.innerHTML = "";

  if (active === 0) {
    const count = state.jobs.size;
    overall.innerHTML = count > 0 ? `<span class="hint">${count === 1 ? "1 job" : `${count} jobs`}</span>` : "";
    setWindowTitle("");
    return;
  }

  const bar = document.createElement("div");
  bar.className = "bar";
  const fill = document.createElement("div");
  fill.className = "bar-fill";
  fill.style.width = `${(b.fraction * 100).toFixed(1)}%`;
  bar.appendChild(fill);

  const text = document.createElement("span");
  text.className = "num";
  const pct = `${Math.floor(b.fraction * 100)}%`;
  text.textContent = `${b.done} of ${b.total} done · ${pct}`;
  overall.append(bar, text);
  setWindowTitle(pct);
}

function setWindowTitle(progress) {
  // The window title carries overall progress, so it shows on the taskbar
  // while the window is minimised or behind something else.
  runtimeCall("WindowSetTitle", progress ? `${progress} · Media Tools` : "Media Tools");
}

/* ---------- Inspector ---------- */

function renderInspectPage(inner) {
  inner.appendChild(pageHead("Inspect a File", "See the container, streams, codecs and bitrates of any media file."));

  const el = card("File");
  const zone = document.createElement("button");
  zone.className = "dropzone";
  zone.innerHTML = `<span class="pict">${icon("upload")}</span><span><strong>Choose a File</strong><span class="hint">Or drop one anywhere in the window.</span></span>`;
  zone.addEventListener("click", async () => {
    try {
      const paths = await call("SelectFiles", "video.convert");
      if (paths && paths.length > 0) {
        await loadInfo(paths[0]);
      }
    } catch (err) {
      toast(errorText(err), "bad");
    }
  });
  el.appendChild(zone);
  inner.appendChild(el);

  if (state.info) {
    inner.appendChild(infoCard(state.info));
    inner.appendChild(streamsCard(state.info));
  }
}

async function loadInfo(path) {
  if (!path) {
    return;
  }
  try {
    state.info = await call("Inspect", path);
    if (state.view.kind === "inspect") {
      renderPage();
    }
  } catch (err) {
    toast(errorText(err), "bad");
  }
}

function infoCard(info) {
  const el = card("Container");
  const dl = document.createElement("dl");
  dl.className = "kv";
  addKV(dl, "File", baseName(info.path));
  addKV(dl, "Format", info.format || "Unknown");
  addKV(dl, "Duration", info.durationSeconds > 0 ? clock(info.durationSeconds) : "Unknown");
  addKV(dl, "Size", bytes(info.sizeBytes));
  addKV(dl, "Overall Bitrate", info.bitRate > 0 ? `${Math.round(info.bitRate / 1000)} kbps` : "Unknown");
  if (info.totalFrames > 0) {
    addKV(dl, "Video Frames", info.totalFrames.toLocaleString());
  }
  el.appendChild(dl);
  return el;
}

function streamsCard(info) {
  const streams = info.streams || [];
  const el = card(`Streams (${streams.length})`);
  streams.forEach((stream) => {
    const box = document.createElement("div");
    box.className = "stream";

    const head = document.createElement("div");
    head.className = "stream-head";
    head.innerHTML =
      `<span class="chip accent">${escapeHTML(capitalise(stream.type || "Unknown"))}</span>` +
      `<span>#${stream.index} &middot; ${escapeHTML(stream.codec || "unknown codec")}</span>`;
    box.appendChild(head);

    const dl = document.createElement("dl");
    dl.className = "kv";
    if (stream.width > 0) {
      addKV(dl, "Resolution", `${stream.width} × ${stream.height}`);
    }
    if (stream.frameRate) {
      addKV(dl, "Frame Rate", `${rate(stream.frameRate)} fps`);
    }
    if (stream.sampleRate) {
      addKV(dl, "Sample Rate", `${stream.sampleRate} Hz`);
    }
    if (stream.channels > 0) {
      addKV(dl, "Channels", String(stream.channels));
    }
    if (stream.language) {
      addKV(dl, "Language", stream.language);
    }
    if (stream.bitRate > 0) {
      addKV(dl, "Bitrate", `${Math.round(stream.bitRate / 1000)} kbps`);
    }
    box.appendChild(dl);
    el.appendChild(box);
  });
  return el;
}

function addKV(dl, key, value, mono) {
  const dt = document.createElement("dt");
  dt.textContent = key;
  const dd = document.createElement("dd");
  dd.textContent = value;
  if (mono) {
    dd.className = "mono";
  }
  dl.append(dt, dd);
}

/* ---------- Settings ---------- */

function renderSettingsPage(inner) {
  inner.appendChild(pageHead("Settings", "Saved as you change them. Encoding applies to every video tool."));
  inner.appendChild(encodingCard());
  inner.appendChild(encodersCard());
  inner.appendChild(outputSettingsCard());
  inner.appendChild(speechCard());
  inner.appendChild(notificationsCard());
  inner.appendChild(shortcutsCard());
  inner.appendChild(environmentCard());
}

function encodingCard() {
  const el = card("Encoding");
  const grid = document.createElement("div");
  grid.className = "grid";

  const available = (state.env.backends || []).filter((b) => b.available);
  grid.appendChild(
    settingField("Encoder", "Hardware encoders are much faster. Software gives the smallest file for its quality.", (id) =>
      makeSelect({
        labelledBy: id,
        choices: available.map((b) => ({ value: b.id, label: b.label, hint: b.vendor === "cpu" ? "Software" : "Hardware" })),
        value: state.settings.backendId,
        empty: "None Detected",
        onChange: (v) => saveSetting("backendId", v),
      })
    )
  );
  grid.appendChild(
    settingField("GPU", "Which card to use when there are several.", (id) =>
      makeSelect({
        labelledBy: id,
        choices: (state.env.devices || []).map((d) => ({
          value: String(d.index),
          label: d.name,
          hint: d.index >= 0 ? `Device ${d.index}` : "",
        })),
        value: String(state.settings.deviceIndex),
        onChange: (v) => saveSetting("deviceIndex", parseInt(v, 10)),
      })
    )
  );
  grid.appendChild(
    settingField("Quality", "Lower looks better and makes a bigger file. 18 is visually lossless.", (id) =>
      makeSlider({ labelledBy: id, min: 0, max: 51, step: 1, value: state.settings.quality, onInput: (v) => saveSetting("quality", parseInt(v, 10)) })
    )
  );
  grid.appendChild(
    settingField("Preset", "Slower presets make smaller files at the same quality.", (id) =>
      makeSelect({
        labelledBy: id,
        choices: PRESETS,
        value: state.settings.preset,
        onChange: (v) => saveSetting("preset", v),
      })
    )
  );
  grid.appendChild(
    settingField("Audio Bitrate", "Used when audio is re-encoded to AAC.", (id) =>
      makeSlider({ labelledBy: id, min: 64, max: 512, step: 32, unit: "kbps", value: state.settings.audioBitrateKb, onInput: (v) => saveSetting("audioBitrateKb", parseInt(v, 10)) })
    )
  );
  grid.appendChild(
    settingField("Jobs at Once", "Takes effect the next time the app starts.", (id) =>
      makeSlider({ labelledBy: id, min: 1, max: 8, step: 1, value: state.settings.concurrency, onInput: (v) => saveSetting("concurrency", parseInt(v, 10)) })
    )
  );
  el.appendChild(grid);
  return el;
}

function encodersCard() {
  const el = card("Detected Encoders", "Each one was checked by running a real test encode on this machine.");
  const again = document.createElement("button");
  again.className = "btn";
  again.innerHTML = `${icon("refresh")}Detect Again`;
  again.addEventListener("click", async () => {
    again.disabled = true;
    again.lastChild.textContent = "Detecting";
    try {
      state.env = await call("RedetectHardware");
      state.settings = await call("GetSettings");
      renderStatus();
      refreshPage();
      setStatus("good", "Encoders checked again.");
    } catch (err) {
      toast(errorText(err), "bad");
      again.disabled = false;
    }
  });
  el.querySelector(".card-head").appendChild(again);

  (state.env.backends || []).forEach((backend) => {
    const row = document.createElement("div");
    row.className = "backend";
    row.innerHTML =
      `<div><div class="name">${escapeHTML(backend.label)}</div>` +
      `<div class="detail">${escapeHTML(capitalise(backend.detail || ""))}</div></div>` +
      `<span class="chip ${backend.available ? "good" : "muted"}">${backend.available ? "Available" : "Unavailable"}</span>`;
    el.appendChild(row);
  });
  return el;
}

function outputSettingsCard() {
  const el = card("Output");
  const grid = document.createElement("div");
  grid.className = "grid";
  grid.appendChild(
    settingField("Save Files", "A tool can still pick a different folder for one run.", (id) =>
      makeSelect({
        labelledBy: id,
        choices: [
          { value: "alongside", label: "Next to the Original" },
          { value: "fixed", label: "In One Folder" },
        ],
        value: state.settings.outputMode,
        onChange: async (v) => {
          await saveSetting("outputMode", v);
          refreshPage();
        },
      })
    )
  );
  grid.appendChild(
    settingField("If a File Exists", "", (id) =>
      makeSelect({
        labelledBy: id,
        choices: [
          { value: "suffix", label: "Keep Both", hint: "Adds (1) to the new name" },
          { value: "overwrite", label: "Replace It" },
        ],
        value: state.settings.overwritePolicy === "overwrite" ? "overwrite" : "suffix",
        onChange: (v) => saveSetting("overwritePolicy", v),
      })
    )
  );
  el.appendChild(grid);

  if (state.settings.outputMode === "fixed") {
    const row = document.createElement("div");
    row.className = "dest";
    row.style.marginTop = "14px";
    const path = document.createElement("span");
    path.className = state.settings.outputDir ? "path mono" : "path hint";
    path.textContent = state.settings.outputDir || "No folder chosen yet.";
    const pick = document.createElement("button");
    pick.className = "btn";
    pick.innerHTML = `${icon("folder")}Choose Folder`;
    pick.addEventListener("click", async () => {
      try {
        const dir = await call("SelectFolder", "global.out");
        if (dir) {
          await saveSetting("outputDir", dir);
          refreshPage();
        }
      } catch (err) {
        toast(errorText(err), "bad");
      }
    });
    row.append(path, pick);
    el.appendChild(row);
  }
  return el;
}

function notificationsCard() {
  const el = card("Notifications");
  const label = "Notify Me When a Batch Finishes";
  const row = document.createElement("div");
  row.className = "toggle-row";
  row.appendChild(makeToggle(label, Boolean(state.settings.notifications), (v) => saveSetting("notifications", v)));
  el.appendChild(row);
  addInfo(
    row,
    el,
    label,
    "Only for batches that take longer than 15 seconds. On Windows, turning this on registers Media Tools so it's allowed to show notifications.",
    "set-notifications"
  );
  return el;
}

/* ---------- Speech models ----------
 * Downloading a model is the app's only network request apart from the
 * download tools, and it happens only from this card. */

function speechCard() {
  const speech = (state.env && state.env.speech) || { models: [] };
  const el = card(
    "Speech Models",
    "Transcribe to Subtitles uses one of these. Each downloads once from Hugging Face, then runs on this computer."
  );
  const choose = document.createElement("button");
  choose.className = "btn";
  choose.innerHTML = `${icon("folder")}Choose Model File`;
  choose.dataset.tip = "Use a whisper.cpp model you already have";
  choose.addEventListener("click", () => speechAction("ChooseSpeechModelFile"));
  el.querySelector(".card-head").appendChild(choose);
  if (!speechSupported()) {
    el.classList.add("warn");
    const note = document.createElement("div");
    note.className = "hint model-note";
    note.textContent =
      "Your ffmpeg can't use these yet. Transcribing needs version 8.0 or later with whisper, such as the full build from gyan.dev.";
    el.appendChild(note);
  }

  (speech.models || []).forEach((m) => el.appendChild(modelRow(m, speech)));

  const own = speech.active && !(speech.models || []).some((m) => m.installed && samePath(m.path, speech.active));
  if (own) {
    const row = document.createElement("div");
    row.className = "backend model";
    row.innerHTML =
      `<div><div class="name">Your Own Model</div><div class="detail mono">${escapeHTML(speech.active)}</div></div>` +
      `<span class="chip accent">In Use</span>`;
    el.appendChild(row);
  }
  if (speech.folder) {
    const where = document.createElement("div");
    where.className = "hint model-folder";
    where.innerHTML = `Downloads are kept in <span class="mono">${escapeHTML(speech.folder)}</span>`;
    el.appendChild(where);
  }
  return el;
}

function modelRow(m, speech) {
  const row = document.createElement("div");
  row.className = "backend model";
  row.dataset.model = m.id;
  row.innerHTML =
    `<div><div class="name">${escapeHTML(m.name)}</div>` +
    `<div class="detail">${escapeHTML(m.hint)} &middot; ${bytes(m.sizeBytes)}</div></div>`;

  const actions = document.createElement("div");
  actions.className = "model-actions";
  if (speech.downloading === m.id) {
    const p = state.speechProgress && state.speechProgress.id === m.id ? state.speechProgress : { received: 0, total: m.sizeBytes };
    const bar = document.createElement("div");
    bar.className = "bar model-bar";
    bar.innerHTML = `<div class="bar-fill" style="width:${percentOf(p)}%"></div>`;
    const got = document.createElement("span");
    got.className = "model-progress";
    got.textContent = `${bytes(p.received)} of ${bytes(p.total)}`;
    actions.append(got, bar, smallButton("Cancel", () => call("CancelSpeechDownload").catch(() => {})));
  } else if (m.installed) {
    if (samePath(m.path, speech.active)) {
      actions.appendChild(chip("In Use", "accent"));
    } else {
      actions.appendChild(smallButton("Use", () => speechAction("UseSpeechModel", m.id)));
    }
    actions.appendChild(
      confirmButton("Remove", "Click Again to Remove", () => speechAction("RemoveSpeechModel", m.id, `Removed the ${m.name} model.`))
    );
  } else {
    const get = smallButton("Download", () => downloadModel(m));
    get.disabled = Boolean(speech.downloading);
    actions.appendChild(get);
  }
  row.appendChild(actions);
  return row;
}

async function downloadModel(m) {
  try {
    await call("DownloadSpeechModel", m.id);
    state.env.speech.downloading = m.id;
    state.speechProgress = { id: m.id, received: 0, total: m.sizeBytes };
    refreshPage();
  } catch (err) {
    toast(errorText(err), "bad");
  }
}

/* speechAction runs a model command that answers with the new status. */
async function speechAction(name, id, done) {
  try {
    const status = id === undefined ? await call(name) : await call(name, id);
    if (status) {
      state.env.speech = status;
    }
    refreshPage();
    if (done) {
      toast(done, "good");
    }
  } catch (err) {
    toast(errorText(err), "bad");
  }
}

function onSpeechProgress(p) {
  state.speechProgress = p;
  const row = document.querySelector(`.model[data-model="${CSS.escape(p.id)}"]`);
  if (!row) {
    return;
  }
  const fill = row.querySelector(".bar-fill");
  const got = row.querySelector(".model-progress");
  if (fill) {
    fill.style.width = `${percentOf(p)}%`;
  }
  if (got) {
    got.textContent = `${bytes(p.received)} of ${bytes(p.total)}`;
  }
}

async function onSpeechDone(result) {
  state.speechProgress = null;
  try {
    state.env = await call("GetEnvironment");
  } catch (err) {
    // The old status stays until the next refresh.
  }
  const model = ((state.env && state.env.speech && state.env.speech.models) || []).find((m) => m.id === result.id);
  const name = model ? model.name : "speech";
  if (!result.error) {
    toast(`The ${name} model is ready to use.`, "good");
  } else if (result.error !== "canceled") {
    toast(`The ${name} model couldn't be downloaded: ${result.error}`, "bad");
  }
  if (state.view.kind === "settings" || state.view.kind === "tool") {
    refreshPage();
  }
}

function percentOf(p) {
  return p.total > 0 ? Math.min(100, (p.received / p.total) * 100).toFixed(1) : 0;
}

function samePath(a, b) {
  const norm = (p) => (p || "").replace(/\\/g, "/").toLowerCase();
  return Boolean(a) && norm(a) === norm(b);
}

function smallButton(label, onClick) {
  const button = document.createElement("button");
  button.className = "btn small";
  button.textContent = label;
  button.addEventListener("click", onClick);
  return button;
}

function chip(text, tone) {
  const el = document.createElement("span");
  el.className = `chip ${tone || ""}`;
  el.textContent = text;
  return el;
}

/* confirmButton asks for a second click within a few seconds before doing
 * something that can't be undone. */
function confirmButton(label, confirmLabel, action) {
  let timer = 0;
  const button = smallButton(label, () => {
    if (!timer) {
      button.textContent = confirmLabel;
      timer = setTimeout(() => {
        timer = 0;
        button.textContent = label;
      }, 4000);
      return;
    }
    clearTimeout(timer);
    timer = 0;
    action();
  });
  return button;
}

function shortcutsCard() {
  const el = card("Keyboard Shortcuts");
  const dl = document.createElement("dl");
  dl.className = "kv";
  addKV(dl, "Ctrl+Enter", "Run the current tool");
  addKV(dl, "Ctrl+O", "Choose files");
  addKV(dl, "Ctrl+K", "Find a tool");
  addKV(dl, "Ctrl+,", "Open settings");
  el.appendChild(dl);
  return el;
}

function environmentCard() {
  const el = card("Environment");
  const dl = document.createElement("dl");
  dl.className = "kv";
  addKV(dl, "ffmpeg", state.env.ffmpegPath || "Not found", true);
  addKV(dl, "ffprobe", state.env.ffprobePath || "Not found", true);
  addKV(dl, "yt-dlp", state.env.ytdlpPath || "Not found", true);
  addKV(dl, "Settings File", state.env.settingsPath || "Unknown", true);
  el.appendChild(dl);
  return el;
}

function settingField(label, help, build) {
  const field = document.createElement("div");
  field.className = "field";
  const id = `set-${label.replace(/\W+/g, "-").toLowerCase()}`;
  const head = fieldLabel(id, label);
  field.append(head, build(id));
  addInfo(head, field, label, help, id);
  return field;
}

async function saveSetting(key, value) {
  state.settings[key] = value;
  renderStatus();
  scheduleSave();
}

/* ---------- Saving ----------
 * Settings are written a moment after the last change rather than on every
 * slider tick or keystroke. A generation counter keeps a slow reply from an
 * earlier save overwriting a newer local change. */

let saveTimer = 0;
let saveGeneration = 0;

function scheduleSave() {
  saveGeneration += 1;
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flushSave, LIMITS.saveDelayMs);
}

async function flushSave() {
  clearTimeout(saveTimer);
  saveTimer = 0;
  if (!state.settings) {
    return;
  }
  const generation = saveGeneration;
  try {
    const saved = await call("SaveSettings", state.settings);
    if (generation === saveGeneration && saved) {
      state.settings = saved;
    }
  } catch (err) {
    toast(`Couldn't save your settings: ${errorText(err)}`, "bad");
  }
}

/* ---------- Status strip ---------- */

function renderStatus() {
  if (!state.env || !state.settings) {
    return;
  }
  const b = batchProgress();
  if (!state.env.ffmpegPath) {
    setStatus("bad", "ffmpeg wasn't found, so only the download tools will work.");
  } else if (b.running + b.queued > 0) {
    const waiting = b.queued > 0 ? `, ${b.queued} waiting` : "";
    setStatus("good", `Running ${b.running === 1 ? "1 job" : `${b.running} jobs`}${waiting}.`);
  } else if (b.total > 0) {
    const failed = b.failed > 0 ? `, ${b.failed} failed` : "";
    setStatus(b.failed > 0 ? "warn" : "good", `Finished ${b.total === 1 ? "1 job" : `${b.total} jobs`}${failed}.`);
  } else {
    setStatus("good", "Ready.");
  }

  const backend = activeBackend();
  const facts = [];
  if (backend) {
    facts.push(backend.label);
  }
  facts.push(`quality ${state.settings.quality}`, `${state.settings.preset} preset`);
  if (!state.env.ytdlpPath) {
    facts.push("yt-dlp not found");
  }
  document.getElementById("status-facts").textContent = facts.join(" · ");
}

function setStatus(tone, text) {
  const dot = document.getElementById("status-dot");
  dot.className = `dot ${tone === "good" ? "" : tone}`;
  document.getElementById("status-line").textContent = text;
}

function activeBackend() {
  if (!state.env || !state.settings) {
    return null;
  }
  const backends = state.env.backends || [];
  return backends.find((b) => b.id === state.settings.backendId && b.available) || backends.find((b) => b.available) || null;
}

/* ---------- Controls ----------
 * Every control is drawn here in the house style. Dropdowns in particular
 * are built rather than native, since the platform popup is white with a
 * blue highlight whatever the page says. */

let openPopup = null;

function makeSelect({ labelledBy, choices, value, onChange, empty }) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "select-button";
  button.setAttribute("aria-haspopup", "listbox");
  button.setAttribute("aria-expanded", "false");
  if (labelledBy) {
    button.setAttribute("aria-labelledby", `${labelledBy} ${labelledBy}-value`);
  }

  let current = value;
  const paint = () => {
    const choice = choices.find((c) => c.value === current) || choices[0];
    if (!choice) {
      button.innerHTML = `<span class="value">${escapeHTML(empty || "Nothing to Choose")}</span>`;
      button.disabled = true;
      return;
    }
    button.innerHTML =
      `<span class="value" id="${labelledBy}-value">${escapeHTML(choice.label)}</span>` +
      (choice.hint ? `<span class="value-hint">${escapeHTML(choice.hint)}</span>` : "");
  };
  paint();

  const choose = (v) => {
    closePopup();
    button.focus();
    if (v === current) {
      return;
    }
    current = v;
    paint();
    onChange(v);
  };

  button.addEventListener("click", () => {
    if (openPopup && openPopup.owner === button) {
      closePopup();
      return;
    }
    showPopup(button, choices, current, choose);
  });
  button.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      showPopup(button, choices, current, choose);
    }
  });
  return button;
}

/* showPopup draws a listbox under owner
 * With keepFocus the owner, such as the find field, keeps typing focus and
 * forwards its keys through onKey */
function showPopup(owner, choices, current, choose, opts) {
  const o = opts || {};
  closePopup();
  const list = document.createElement("div");
  list.className = o.className ? `popup ${o.className}` : "popup";
  list.setAttribute("role", "listbox");
  list.tabIndex = -1;

  let focused = Math.max(0, choices.findIndex((c) => c.value === current));
  const items = choices.map((choice, index) => {
    const item = document.createElement("div");
    item.className = "option";
    item.setAttribute("role", "option");
    item.setAttribute("aria-selected", String(choice.value === current));
    item.innerHTML =
      (choice.pict ? `<span class="option-pict">${choice.pict}</span>` : "") +
      `<span>${escapeHTML(choice.label)}</span>` +
      (choice.hint ? `<span class="option-hint">${escapeHTML(choice.hint)}</span>` : "");
    item.addEventListener("mousedown", (e) => e.preventDefault());
    item.addEventListener("click", () => choose(choice.value));
    item.addEventListener("mousemove", () => setFocus(index));
    list.appendChild(item);
    return item;
  });
  if (items.length === 0) {
    const empty = document.createElement("div");
    empty.className = "popup-empty";
    empty.textContent = o.empty || "Nothing to choose.";
    list.appendChild(empty);
  }

  function setFocus(index) {
    if (items.length === 0) {
      return;
    }
    items[focused] && items[focused].classList.remove("focused");
    focused = Math.max(0, Math.min(items.length - 1, index));
    items[focused].classList.add("focused");
    items[focused].scrollIntoView({ block: "nearest" });
  }

  function onKey(e) {
    if (e.key === "ArrowDown") {
      setFocus(focused + 1);
    } else if (e.key === "ArrowUp") {
      setFocus(focused - 1);
    } else if (e.key === "Home" && !o.keepFocus) {
      setFocus(0);
    } else if (e.key === "End" && !o.keepFocus) {
      setFocus(items.length - 1);
    } else if (e.key === "Enter" || (e.key === " " && !o.keepFocus)) {
      if (items.length > 0) {
        choose(choices[focused].value);
      }
    } else if (e.key === "Escape" || e.key === "Tab") {
      closePopup();
      if (!o.keepFocus) {
        owner.focus();
      }
      return e.key === "Escape";
    } else {
      return false;
    }
    return true;
  }

  list.addEventListener("keydown", (e) => {
    if (onKey(e)) {
      e.preventDefault();
      e.stopPropagation();
    }
  });
  if (o.keepFocus) {
    // Pressing on the list's padding or scrollbar mustn't take typing focus
    list.addEventListener("mousedown", (e) => e.preventDefault());
  }

  document.body.appendChild(list);
  placePopup(owner, list);
  owner.setAttribute("aria-expanded", "true");
  openPopup = { owner, list, onKey };
  setFocus(focused);
  if (!o.keepFocus) {
    list.focus();
  }
}

function placePopup(owner, list) {
  const rect = owner.getBoundingClientRect();
  list.style.minWidth = `${rect.width}px`;
  list.style.left = `${rect.left}px`;
  const below = window.innerHeight - rect.bottom - 8;
  const height = Math.min(list.scrollHeight, 320);
  if (below >= height || below >= rect.top) {
    list.style.top = `${rect.bottom + 4}px`;
    list.style.maxHeight = `${Math.max(120, below - 4)}px`;
  } else {
    list.style.maxHeight = `${Math.max(120, rect.top - 12)}px`;
    list.style.top = `${Math.max(8, rect.top - 4 - Math.min(height, rect.top - 12))}px`;
  }
  // Keep the popup on screen horizontally.
  const overflow = rect.left + list.offsetWidth - (window.innerWidth - 8);
  if (overflow > 0) {
    list.style.left = `${Math.max(8, rect.left - overflow)}px`;
  }
}

function closePopup() {
  if (!openPopup) {
    return;
  }
  openPopup.owner.setAttribute("aria-expanded", "false");
  openPopup.list.remove();
  openPopup = null;
}

function makeToggle(label, checked, onChange) {
  const wrap = document.createElement("label");
  wrap.className = "toggle";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.setAttribute("role", "switch");
  input.checked = checked;
  input.addEventListener("change", (e) => onChange(e.target.checked));
  const text = document.createElement("span");
  text.textContent = label;
  wrap.append(input, text);
  return wrap;
}

function makeSlider({ labelledBy, min, max, step, value, unit, onInput }) {
  const row = document.createElement("div");
  row.className = "slider-row";

  const range = document.createElement("input");
  range.type = "range";
  range.className = "slider";
  range.min = min;
  range.max = max;
  range.step = step;
  range.value = value;
  if (labelledBy) {
    range.setAttribute("aria-labelledby", labelledBy);
  }

  const readout = document.createElement("span");
  readout.className = "slider-value";

  const paint = () => {
    const pct = ((parseFloat(range.value) - min) / (max - min)) * 100;
    range.style.setProperty("--fill", `${pct}%`);
    readout.textContent = unit ? `${range.value} ${unit}` : range.value;
  };
  paint();
  range.addEventListener("input", () => {
    paint();
    onInput(range.value);
  });

  row.append(range, readout);
  return row;
}

function makeNumber({ labelledBy, min, max, step, value, onInput }) {
  const input = document.createElement("input");
  input.className = "input";
  input.type = "number";
  if (min !== undefined) {
    input.min = min;
    input.max = max;
    input.step = step;
  }
  input.value = value;
  if (labelledBy) {
    input.setAttribute("aria-labelledby", labelledBy);
  }
  input.addEventListener("input", () => {
    const v = parseFloat(input.value);
    const bad = input.value === "" || Number.isNaN(v) || (min !== undefined && (v < min || v > max));
    input.classList.toggle("invalid", bad);
    if (!bad) {
      onInput(input.value);
    }
  });
  return input;
}

function makeText({ labelledBy, value, onInput }) {
  const input = document.createElement("input");
  input.className = "input";
  input.type = "text";
  input.value = value || "";
  input.spellcheck = false;
  if (labelledBy) {
    input.setAttribute("aria-labelledby", labelledBy);
  }
  input.addEventListener("input", () => onInput(input.value));
  return input;
}

/* ---------- Global wiring ---------- */

function wireGlobalControls() {
  document.getElementById("btn-cancel-all").addEventListener("click", async () => {
    try {
      await call("CancelAllJobs");
    } catch (err) {
      toast(errorText(err), "bad");
    }
  });
  document.getElementById("btn-clear").addEventListener("click", async () => {
    try {
      await call("ClearFinishedJobs");
      await refreshJobs();
    } catch (err) {
      toast(errorText(err), "bad");
    }
  });
  const filter = document.getElementById("tool-filter");
  filter.addEventListener("input", () => {
    state.filter = filter.value;
    showFinder();
  });
  filter.addEventListener("focus", () => {
    if (state.filter.trim()) {
      showFinder();
    }
  });
  filter.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      clearFinder();
      e.preventDefault();
      return;
    }
    // The field keeps the focus while the list is open, so its keys steer it,
    // and a key the list used stops here so Ctrl+Enter can't also run a job
    if (openPopup && openPopup.owner === filter && openPopup.onKey(e)) {
      e.preventDefault();
      e.stopPropagation();
    }
  });

  document.addEventListener("keydown", onShortcut);
  document.addEventListener("mousedown", (e) => {
    if (openPopup && !openPopup.list.contains(e.target) && !openPopup.owner.contains(e.target)) {
      closePopup();
    }
  });
  window.addEventListener("resize", () => {
    closePopup();
    fitBand();
  });
  // Scroll events don't bubble, so the page column listens in the capture phase
  document.getElementById("page").addEventListener("scroll", closePopup, { passive: true, capture: true });

  wireDragHighlight();
  wireTooltips();
}

function onShortcut(e) {
  if (!e.ctrlKey && !e.metaKey) {
    return;
  }
  const key = e.key.toLowerCase();
  if (key === "k" || key === "f") {
    e.preventDefault();
    const find = document.getElementById("tool-filter");
    find.focus();
    find.select();
  } else if (key === "enter") {
    e.preventDefault();
    submit(currentTool());
  } else if (key === "o") {
    const tool = currentTool();
    if (tool && tool.input !== "url") {
      e.preventDefault();
      pickFiles(tool);
    }
  } else if (key === ",") {
    e.preventDefault();
    openView("settings", "settings");
  }
}

function wireDragHighlight() {
  // Wails delivers the paths through OnFileDrop; these events only light up
  // the drop zone while something is being dragged over the window.
  let depth = 0;
  const isFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  document.addEventListener("dragenter", (e) => {
    if (isFiles(e)) {
      depth += 1;
      document.body.classList.add("dragging");
    }
  });
  document.addEventListener("dragleave", (e) => {
    if (isFiles(e)) {
      depth = Math.max(0, depth - 1);
      if (depth === 0) {
        document.body.classList.remove("dragging");
      }
    }
  });
  document.addEventListener("drop", () => {
    depth = 0;
    document.body.classList.remove("dragging");
  });
}

function wireTooltips() {
  // Native title tooltips are drawn by the platform; this one is themed.
  let tip = null;
  let timer = 0;
  // A click can hide its own button, as Fold does, so a pending tip would
  // land at the corner of the window
  document.addEventListener(
    "click",
    () => {
      clearTimeout(timer);
      if (tip) {
        tip.remove();
        tip = null;
      }
    },
    true
  );
  document.addEventListener("mouseover", (e) => {
    const target = e.target.closest && e.target.closest("[data-tip]");
    clearTimeout(timer);
    if (tip) {
      tip.remove();
      tip = null;
    }
    if (!target || (target.dataset.tipWhen === "folded" && !target.closest(".folded"))) {
      return;
    }
    timer = setTimeout(() => {
      tip = document.createElement("div");
      tip.className = "tooltip";
      tip.textContent = target.dataset.tip;
      document.body.appendChild(tip);
      const rect = target.getBoundingClientRect();
      const left = Math.min(rect.left, window.innerWidth - tip.offsetWidth - 8);
      const top = rect.bottom + 6 + tip.offsetHeight > window.innerHeight ? rect.top - tip.offsetHeight - 6 : rect.bottom + 6;
      tip.style.left = `${Math.max(8, left)}px`;
      tip.style.top = `${top}px`;
    }, 450);
  });
}

/* ---------- Banners ---------- */

function toast(message, tone, timeout) {
  const host = document.getElementById("toasts");
  while (host.children.length >= LIMITS.toasts) {
    host.removeChild(host.firstChild);
  }

  const el = document.createElement("div");
  el.className = `toast ${tone || ""}`;
  const pict = document.createElement("span");
  pict.className = "pict";
  pict.innerHTML = icon(tone === "good" ? "check" : tone === "warn" || tone === "bad" ? "alert" : "info");
  el.appendChild(pict);
  const text = document.createElement("span");
  text.className = "text";
  text.textContent = message;
  const close = document.createElement("button");
  close.className = "icon-btn";
  close.innerHTML = icon("close");
  close.setAttribute("aria-label", "Dismiss");
  close.addEventListener("click", () => el.remove());
  el.append(text, close);
  host.appendChild(el);

  const delay = timeout === undefined ? (tone === "bad" ? 9000 : 5000) : timeout;
  if (delay > 0) {
    setTimeout(() => el.remove(), delay);
  }
}

/* ---------- Formatting ---------- */

function clock(seconds) {
  if (!isFinite(seconds) || seconds < 0) {
    return "--:--";
  }
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (v) => String(v).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

function bytes(value) {
  if (!value || value <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size.toFixed(size >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function rate(expression) {
  const parts = String(expression).split("/");
  if (parts.length !== 2) {
    return expression;
  }
  const den = parseFloat(parts[1]);
  if (!den) {
    return expression;
  }
  return (parseFloat(parts[0]) / den).toFixed(2).replace(/\.00$/, "");
}

function baseName(path) {
  const parts = String(path).split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

function dirName(path) {
  const text = String(path);
  const cut = Math.max(text.lastIndexOf("\\"), text.lastIndexOf("/"));
  return cut > 0 ? text.slice(0, cut) : "";
}

function extName(path) {
  const name = baseName(path);
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

function capitalise(text) {
  const s = String(text);
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

function escapeHTML(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

window.addEventListener("DOMContentLoaded", boot);
