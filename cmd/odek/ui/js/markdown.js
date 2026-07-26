// Markdown → HTML (safe, no DOMPurify needed since we control input).
// Imports only from utils.js. The copy button on code blocks has no inline
// handler — clicks are delegated on #messages in render.js.
import { escapeHtml } from './utils.js';

export function markdownToHtml(text) {
  if (!text) return '';

  let html = escapeHtml(text);

  // Headers (must be at start of line)
  html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');

  // Horizontal rules
  html = html.replace(/^(---|\*\*\*|___)$/gm, '<hr>');

  // Code blocks (```lang ... ```) — need to handle BEFORE inline code
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (match, lang, code) => {
    const langLabel = lang || 'code';
    return '<div class="code-block">' +
      '<div class="cb-header">' +
        '<span class="cb-lang">' + escapeHtml(langLabel) + '</span>' +
        '<span class="cb-copy">📋 copy</span>' +
      '</div>' +
      '<pre><code>' + escapeHtml(code) + '</code></pre>' +
    '</div>';
  });

  // Inline code
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');

  // Bold
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');

  // Italic
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');

  // Strikethrough
  html = html.replace(/~~(.+?)~~/g, '<s>$1</s>');

  // Links — allowlist safe URL schemes to prevent javascript:/data: XSS.
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (match, label, url) => {
    const trimmed = url.trim();
    const safe = /^(https?:|mailto:|\/|#|\.\/|\.\.\/)/i.test(trimmed);
    if (!safe) return label;
    return '<a href="' + trimmed.replace(/"/g, '&quot;') + '" target="_blank" rel="noopener noreferrer">' + label + '</a>';
  });

  // Unordered lists (simple: lines starting with - or *)
  html = html.replace(/^[\s]*[-*]\s+(.+)$/gm, '<li>$1</li>');
  html = html.replace(/(<li>.*<\/li>\n?)+/g, '<ul>$&</ul>');

  // Paragraphs — wrap remaining non-tag text in <p>
  // Split by double newlines (paragraph breaks)
  const parts = html.split(/\n\n+/);
  html = parts.map(part => {
    part = part.trim();
    if (!part) return '';
    // Don't wrap if it starts with a block-level tag
    if (/^<(h[1-4]|ul|ol|li|pre|div|hr|table)/.test(part)) return part;
    // Don't wrap single <br>
    if (/^<br\s*\/?>$/.test(part)) return part;
    return '<p>' + part.replace(/\n/g, '<br>') + '</p>';
  }).join('\n');

  return html;
}
