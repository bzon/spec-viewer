function slugify(text) {
  return text.toLowerCase().replace(/[^\w\s-]/g, '').replace(/\s+/g, '-');
}

export function buildIndex(files) {
  return files.map(({ path: filePath, name: fileName, content }) => {
    const lines = content.split('\n');
    const headings = [];
    for (const line of lines) {
      const m = line.match(/^(#{2,3})\s+(.+)$/);
      if (m) headings.push({ level: m[1].length, text: m[2], id: slugify(m[2]) });
    }
    const displayName = fileName.replace(/\.md$/i, '').replace(/[-_]/g, ' ');
    return { filePath, fileName, displayName, headings, contentLines: lines };
  });
}

function score(target, query) {
  const t = target.toLowerCase();
  const q = query.toLowerCase();
  if (t === q) return 100;
  if (t.startsWith(q)) return 75;
  if (t.includes(q)) return 50;
  return 0;
}

export function truncateAround(text, query, maxLen = 80) {
  const idx = text.toLowerCase().indexOf(query.toLowerCase());
  if (idx === -1) return text.slice(0, maxLen);
  const before = Math.max(0, idx - 20);
  const after = Math.min(text.length, idx + query.length + 40);
  return (before > 0 ? '...' : '') + text.slice(before, after) + (after < text.length ? '...' : '');
}

export function search(query, index) {
  const q = query.trim().toLowerCase();
  if (!q) return [];

  const results = [];

  for (const entry of index) {
    const fileScore = score(entry.displayName, q);
    if (fileScore > 0) {
      results.push({ type: 'file', display: entry.displayName, filePath: entry.filePath, score: fileScore });
    }
    for (const h of entry.headings) {
      const hs = score(h.text, q);
      if (hs > 0) {
        results.push({ type: 'heading', display: `${entry.fileName} > ${h.text}`, filePath: entry.filePath, headingId: h.id, score: hs });
      }
    }
  }

  if (results.length < 3) {
    const seen = new Set();
    for (const entry of index) {
      if (seen.has(entry.filePath)) continue;
      for (const line of entry.contentLines) {
        if (/^#{2,3}\s/.test(line)) continue;
        if (line.toLowerCase().includes(q)) {
          const s = score(line, q) - 10;
          results.push({ type: 'text', display: `${entry.fileName}: ${truncateAround(line, q)}`, filePath: entry.filePath, score: s });
          seen.add(entry.filePath);
          break;
        }
      }
    }
  }

  return results.sort((a, b) => b.score - a.score).slice(0, 10);
}
