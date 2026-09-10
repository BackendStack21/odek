// A single stroke vocabulary for workspace controls. Paths are trusted constants.
const paths = {
 terminal:'<path d="m5 6 5 6-5 6M13 18h6"/>',
 file:'<path d="M14 3H5v18h14V8zM14 3v5h5M8 12h8M8 16h6"/>',
 edit:'<path d="m4 16 12-12 4 4L8 20H4zM13 7l4 4"/>',
 search:'<circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/>',
 globe:'<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18"/>',
 branch:'<circle cx="6" cy="5" r="2"/><circle cx="18" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><path d="M6 7v10M18 7v2a4 4 0 0 1-4 4H6"/>',
 grid:'<rect x="4" y="4" width="6" height="6" rx="1"/><rect x="14" y="4" width="6" height="6" rx="1"/><rect x="4" y="14" width="6" height="6" rx="1"/><rect x="14" y="14" width="6" height="6" rx="1"/>',
 pin:'<path d="m8 3 8 0-1 6 4 4v2H5v-2l4-4-1-6M12 15v6"/>',
 download:'<path d="M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5"/>',
 close:'<path d="m6 6 12 12M6 18 18 6"/>',
};
export function icon(name) { return '<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'+(paths[name]||paths.grid)+'</svg>'; }
export function toolIcon(name) {
 const kind=/shell|exec|bg_/.test(name)?'terminal':/write|patch|edit|diff/.test(name)?'edit':/read|file/.test(name)?'file':/search|grep|find/.test(name)?'search':/browser|http|web/.test(name)?'globe':/delegate|subagent/.test(name)?'branch':'grid';return icon(kind);
}
