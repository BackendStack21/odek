// HTML escaping helpers. Leaf module with no imports so it can be used from
// markdown.js without pulling in state.js/dom.js (which require a browser).

export function escapeHtml(s) {
  if (s == null || s === false || s === '') return '';
  s = String(s);
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

export function escapeAttr(s) {
  if (s == null || s === false || s === '') return '';
  s = String(s);
  // & must be replaced first — doing it last double-escapes the entities
  // introduced by the quote replacements (&quot; → &amp;quot;).
  return s.replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/'/g,'&#39;')
          .replace(/</g,'&lt;').replace(/>/g,'&gt;');
}
