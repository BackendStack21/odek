// Count formatting shared by every view. DOM-free, so result renderers and
// node tests import it directly.

// plural formats a count with the matching noun (or verb) form:
// plural(1, 'job') → '1 job', plural(1200, 'job') → '1,200 jobs'. Irregular
// forms pass the plural explicitly. The number is grouped as English, like
// the words around it, and always renders as digits.
export function plural(n, one, many = one + 's') {
  const count = Number(n);
  return count.toLocaleString('en-US') + ' ' + (count === 1 ? one : many);
}
