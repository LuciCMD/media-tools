/* Media Tools - the icon set.
 *
 * One set for the whole app: 24px line icons drawn with currentColor, so an
 * icon takes its colour from the text around it and the theme stays in
 * style.css. Tools name their icon in the Go registry; anything unknown falls
 * back to its category's icon.
 */

const ICON_PATHS = {
  logo: '<rect x="3" y="5" width="18" height="14" rx="3"/><path d="M10 9.5v5l4.5-2.5z" fill="currentColor"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-4.2-4.2"/>',
  chevron: '<path d="M6 9l6 6 6-6"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  settings:
    '<path d="M4 7h9M17 7h3M4 17h3M11 17h9"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="17" r="2"/>',
  inspect: '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><path d="M12 7.6v.2" stroke-width="2.4"/>',
  upload: '<path d="M12 15V4M7.5 8.5L12 4l4.5 4.5"/><path d="M4 15v3a2 2 0 002 2h12a2 2 0 002-2v-3"/>',
  folder: '<path d="M3.5 7a2 2 0 012-2h4l2 2.5h7a2 2 0 012 2V17a2 2 0 01-2 2h-13a2 2 0 01-2-2z"/>',
  play: '<path d="M8 5.5v13l10.5-6.5z"/>',
  refresh: '<path d="M19.5 12a7.5 7.5 0 11-2.2-5.3"/><path d="M19.5 4.5v4h-4"/>',
  retry: '<path d="M4.5 12a7.5 7.5 0 102.2-5.3"/><path d="M4.5 4.5v4h4"/>',

  audio: '<path d="M9 18V6l10-2v12"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="16.5" cy="16" r="2.5"/>',
  video: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 5v14M17 5v14M3 9.5h4M3 14.5h4M17 9.5h4M17 14.5h4"/>',
  image: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="M20.5 16l-5-5-8.5 8.5"/>',

  convert: '<path d="M4 8h13M13.5 4.5L17 8l-3.5 3.5"/><path d="M20 16H7M10.5 12.5L7 16l3.5 3.5"/>',
  wave: '<path d="M2.5 12h2l2-5 3 10 3-13 3 13 2-5h4"/>',
  levels: '<path d="M5 20v-6M10 20V9M15 20v-9M20 20V4"/>',
  scissors:
    '<circle cx="6" cy="6.5" r="2.5"/><circle cx="6" cy="17.5" r="2.5"/><path d="M8.2 7.8L20 17M8.2 16.2L20 7"/>',
  note: '<path d="M9 18V5l10-1.5V16"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="16.5" cy="16" r="2.5"/>',
  gauge: '<path d="M4.5 17a8.5 8.5 0 1115 0"/><path d="M12 13l4-4"/><circle cx="12" cy="13" r="1.2" fill="currentColor"/>',
  download: '<path d="M12 4v11M7.5 10.5L12 15l4.5-4.5"/><path d="M4 19.5h16"/>',
  "note-down": '<path d="M9 15V4l9-1.5V13"/><circle cx="6.5" cy="15" r="2.5"/><path d="M15 17.5l2.5 2.5 2.5-2.5M17.5 15v5"/>',
  "list-down": '<path d="M4 6h11M4 11h11M4 16h6"/><path d="M14.5 17.5L17 20l2.5-2.5M17 12v8"/>',
  resize: '<path d="M14 4h6v6M10 20H4v-6M20 4l-7 7M4 20l7-7"/>',
  icon: '<rect x="4" y="4" width="16" height="16" rx="4"/><rect x="8.5" y="8.5" width="7" height="7" rx="1.5"/>',
  eraser: '<path d="M8 20h12M5.5 14.5l8-8a2 2 0 012.8 0l2.2 2.2a2 2 0 010 2.8L11 19H8l-2.5-2.5a1.4 1.4 0 010-2z"/>',
  text: '<path d="M5 7V5h14v2M12 5v14M9 19h6"/>',
  compress: '<path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5"/>',
  monitor: '<rect x="3" y="4.5" width="18" height="12" rx="2"/><path d="M8.5 20h7M12 16.5V20"/>',
  crop: '<path d="M6 2.5V16a2 2 0 002 2h13.5"/><path d="M2.5 6H16a2 2 0 012 2v13.5"/>',
  rotate: '<path d="M20 11a8 8 0 10-2.4 5.7"/><path d="M20 4.5V11h-6.5"/>',
  gif: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M11 9.5v5M14.5 14.5v-5H17M14.5 12h2M9 10H7.5a1 1 0 00-1 1v2a1 1 0 001 1H9v-2h-1"/>',
  mute: '<path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z"/><path d="M16 9.5l5 5M21 9.5l-5 5"/>',
  captions: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 11h4M13 11h4M7 15h7M16 15h1"/>',
  "captions-out": '<rect x="3" y="5" width="13" height="14" rx="2"/><path d="M6.5 11h6M6.5 15h4"/><path d="M16 12h6M19.5 9.5L22 12l-2.5 2.5"/>',
  grid: '<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M9.3 4v16M14.7 4v16M4 9.3h16M4 14.7h16"/>',
  layers: '<path d="M12 3.5l8.5 4.5-8.5 4.5L3.5 8z"/><path d="M3.5 12l8.5 4.5 8.5-4.5M3.5 16l8.5 4.5 8.5-4.5"/>',
  palette:
    '<path d="M12 3.5a8.5 8.5 0 000 17c1.2 0 1.8-.9 1.4-2-.5-1.3.4-2.5 1.8-2.5H18a2.5 2.5 0 002.5-2.5A8.5 8.5 0 0012 3.5z"/><circle cx="7.8" cy="11" r="1.1" fill="currentColor"/><circle cx="10.5" cy="7.3" r="1.1" fill="currentColor"/><circle cx="15" cy="7.8" r="1.1" fill="currentColor"/>',
  target: '<circle cx="12" cy="12" r="8.5"/><circle cx="12" cy="12" r="4.5"/><circle cx="12" cy="12" r="1" fill="currentColor"/>',
  box: '<path d="M3.5 7.5L12 3.5l8.5 4v9L12 20.5l-8.5-4z"/><path d="M3.5 7.5l8.5 4 8.5-4M12 11.5v9"/>',
  join: '<rect x="2.5" y="7" width="7.5" height="10" rx="1.5"/><rect x="14" y="7" width="7.5" height="10" rx="1.5"/><path d="M10 12h4"/>',
  noise: '<path d="M3 12h3l1.5-4 2 8 2-4H21"/><path d="M15 8.5h6M17 15.5h4"/>',
  voice:
    '<circle cx="9" cy="8" r="3.5"/><path d="M3 20c.5-3.5 3-5.5 6-5.5s5.5 2 6 5.5"/><path d="M17 6.5a5 5 0 010 7M19.8 4a8.5 8.5 0 010 12"/>',
  transcribe:
    '<path d="M4 5h16a1 1 0 011 1v9a1 1 0 01-1 1h-9l-4.5 3.5V16H4a1 1 0 01-1-1V6a1 1 0 011-1z"/><path d="M7 9h10M7 12h6"/>',
  phone: '<rect x="6.5" y="2.5" width="11" height="19" rx="2.5"/><path d="M10.5 18.5h3"/>',
  "arrow-up": '<path d="M12 19V5M6 11l6-6 6 6"/>',
  "arrow-down": '<path d="M12 5v14M6 13l6 6 6-6"/>',
  info: '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.5"/><path d="M12 7.6v.2" stroke-width="2.4"/>',
  alert: '<path d="M12 4l9 15.5H3z"/><path d="M12 10v4.5"/><path d="M12 17.3v.2" stroke-width="2.4"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  "arrow-right": '<path d="M4.5 12h14M13 6l6 6-6 6"/>',
};

const CATEGORY_ICONS = {
  audio: "audio",
  video: "video",
  image: "image",
  subtitles: "captions",
  download: "download",
};

/** icon returns an inline SVG element string for a named icon. */
function icon(name, fallback) {
  const body = ICON_PATHS[name] || ICON_PATHS[CATEGORY_ICONS[fallback] || fallback] || ICON_PATHS.logo;
  return (
    '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
    'stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    body +
    "</svg>"
  );
}

/** hydrateIcons replaces every [data-icon] placeholder under root. */
function hydrateIcons(root) {
  root.querySelectorAll("[data-icon]").forEach((el) => {
    el.innerHTML = icon(el.getAttribute("data-icon"));
    el.classList.add("slot");
    el.removeAttribute("data-icon");
  });
}
