import markdownit from 'https://esm.sh/markdown-it@14?bundle';
import { encodeHashPath, resolveDocumentTarget, workspaceBase } from './workspace.js';

let highlighter = null;
let mermaid = null;
let currentTheme = 'github-dark';

const themeMap = {
  'github-dark': 'github-dark',
  'github-light': 'github-light',
  'dracula': 'dracula',
  'nord': 'nord',
  'solarized': 'solarized-dark',
};

const md = markdownit({
  html: false,
  linkify: true,
  typographer: true,
  highlight: (code, lang) => {
    if (lang?.toLowerCase() === 'mermaid') {
      return `<pre class="mermaid">${md.utils.escapeHtml(code)}</pre>`;
    }
    if (!highlighter || !lang) return '';
    try {
      return highlighter
        .codeToHtml(code, {
          lang,
          theme: themeMap[currentTheme] || 'github-dark',
        })
        .replace('<pre ', `<pre data-language="${md.utils.escapeHtml(lang)}" `);
    } catch {
      return '';
    }
  },
});

const HEX_COLOR_RE = /^#(?:[0-9a-fA-F]{3}){1,2}$/;
const HEX_COLOR_INLINE_RE = /#(?:[0-9a-fA-F]{3}){1,2}(?=[\s,;)\]}<]|$)/g;

function swatchHtml(hex) {
  return `<span class="color-swatch" style="background:${hex}"></span>${hex}`;
}

function colorSwatchPlugin(mdi) {
  // Inline code spans: `#fb923c`
  const defaultCodeRender = mdi.renderer.rules.code_inline ||
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));

  mdi.renderer.rules.code_inline = (tokens, idx, options, env, self) => {
    const content = tokens[idx].content.trim();
    if (HEX_COLOR_RE.test(content)) {
      return `<code>${swatchHtml(mdi.utils.escapeHtml(content))}</code>`;
    }
    return defaultCodeRender(tokens, idx, options, env, self);
  };

  // Plain text: replace hex colors in text tokens after core parsing
  mdi.core.ruler.after('inline', 'color_swatch_text', (state) => {
    for (const blockToken of state.tokens) {
      if (blockToken.type !== 'inline' || !blockToken.children) continue;
      const newChildren = [];
      for (const token of blockToken.children) {
        if (token.type !== 'text' || !HEX_COLOR_INLINE_RE.test(token.content)) {
          newChildren.push(token);
          continue;
        }
        HEX_COLOR_INLINE_RE.lastIndex = 0;
        let lastIndex = 0;
        let match;
        while ((match = HEX_COLOR_INLINE_RE.exec(token.content)) !== null) {
          if (match.index > lastIndex) {
            const before = new state.Token('text', '', 0);
            before.content = token.content.slice(lastIndex, match.index);
            newChildren.push(before);
          }
          const swatch = new state.Token('html_inline', '', 0);
          swatch.content = swatchHtml(mdi.utils.escapeHtml(match[0]));
          newChildren.push(swatch);
          lastIndex = match.index + match[0].length;
        }
        if (lastIndex < token.content.length) {
          const after = new state.Token('text', '', 0);
          after.content = token.content.slice(lastIndex);
          newChildren.push(after);
        }
      }
      blockToken.children = newChildren;
    }
  });
}

md.use(colorSwatchPlugin);

export async function initRenderer() {
  const { createHighlighter } = await import('https://esm.sh/shiki@3?bundle');
  highlighter = await createHighlighter({
    themes: Object.values(themeMap),
    langs: [
      'javascript', 'typescript', 'go', 'python', 'bash', 'shell',
      'json', 'yaml', 'toml', 'html', 'css', 'sql', 'markdown',
      'rust', 'java', 'ruby', 'php', 'swift', 'kotlin', 'c', 'cpp',
      'diff', 'dockerfile', 'graphql', 'jsx', 'tsx',
    ],
  });
}

// Mermaid draws edge-label backgrounds translucent (`.edgeLabel rect { opacity: 0.5 }`,
// `.labelBkg { background-color: fade(edgeLabelBackground, .5) }`, and the 'default'
// theme's edgeLabelBackground is rgba(232,232,232,0.8)), so the edge line shows through
// the text. Paint them opaque so the label reads as a clean gap in the line. The colour is
// --edge-label-bg, set per label by matchEdgeLabelBackgrounds to the container behind it,
// falling back to the diagram surface (pre.mermaid uses --code-bg). Labels whose rendered
// text is below 4.5:1 on that background also get --edge-label-text (and data-label-text,
// which switches the override on). Injected via themeCSS, which Mermaid scopes to each
// svg; the variables resolve in the page.
const EDGE_LABEL_THEME_CSS = `
  .edgeLabel rect, .edgeLabel .label rect {
    fill: var(--edge-label-bg, var(--code-bg)) !important;
    opacity: 1 !important;
    fill-opacity: 1 !important;
  }
  .edgeLabel, .edgeLabel p, .edgeLabel span, .edgeLabel .label span, .labelBkg {
    background-color: var(--edge-label-bg, var(--code-bg)) !important;
  }
  .edgeLabel[data-label-text], .edgeLabel[data-label-text] * {
    color: var(--edge-label-text) !important;
  }
  .edgeLabel[data-label-text] text, .edgeLabel[data-label-text] tspan {
    fill: var(--edge-label-text) !important;
  }
  .edgeLabel foreignObject { overflow: visible; }
  .labelBkg { box-shadow: -3px 0 0 var(--edge-label-bg, var(--code-bg)), 3px 0 0 var(--edge-label-bg, var(--code-bg)); }
`;

// Relative image and link targets are relative to the document, as in any markdown
// viewer; a leading "/" means the workspace root. markdown-it has already refused
// javascript:, vbscript:, file: and non-image data: URLs, and resolveDocumentTarget
// leaves every URL with a scheme untouched, so only plain paths are rewritten.
function rewriteTargetsPlugin(mdi) {
  const encodePath = (path) => path.split('/').map(encodeURIComponent).join('/');

  const defaultImage = mdi.renderer.rules.image ||
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));
  mdi.renderer.rules.image = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    const resolved = resolveDocumentTarget(token.attrGet('src'), env.docPath || '');
    if (resolved) token.attrSet('src', workspaceBase + encodePath(resolved.path) + resolved.suffix);
    return defaultImage(tokens, idx, options, env, self);
  };

  const defaultLinkOpen = mdi.renderer.rules.link_open ||
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));
  mdi.renderer.rules.link_open = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    const href = token.attrGet('href') || '';
    if (href.startsWith('#')) {
      token.attrSet('data-anchor', href.slice(1)); // scrolls within the document; see app.js
    } else {
      const resolved = resolveDocumentTarget(href, env.docPath || '');
      if (resolved && /\.(md|markdown)$/i.test(resolved.path)) {
        token.attrSet('href', '#' + encodeHashPath(resolved.path)); // opens in the viewer
        const section = resolved.suffix.match(/#(.+)$/);
        if (section) token.attrSet('data-section', section[1]); // scrolled to after load; see app.js
      } else if (resolved) {
        token.attrSet('href', workspaceBase + encodePath(resolved.path) + resolved.suffix);
      }
    }
    return defaultLinkOpen(tokens, idx, options, env, self);
  };
}

md.use(rewriteTargetsPlugin);

export function render(markdownText, docPath = '') {
  return md.render(markdownText, { docPath });
}

// Diagrams the reader expanded, keyed by diagram source plus occurrence among identical
// sources. In memory only; kept across re-renders of a document, dropped on file switch.
const expandedDiagrams = new Set();

export function resetDiagramState() {
  expandedDiagrams.clear();
}

export async function renderDiagrams(container) {
  const nodes = container.querySelectorAll('pre.mermaid');
  if (nodes.length === 0) return;

  // Rendering replaces the diagram source with SVG; keep the source for comment anchoring.
  const occurrences = new Map();
  for (const node of nodes) {
    node.dataset.source = node.textContent;
    const index = occurrences.get(node.dataset.source) ?? 0;
    occurrences.set(node.dataset.source, index + 1);
    node.dataset.diagramKey = `${index}\n${node.dataset.source}`;
  }

  mermaid ||= (await import('https://esm.sh/mermaid@12?bundle')).default;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    suppressErrorRendering: true,
    themeCSS: EDGE_LABEL_THEME_CSS,
    theme: currentTheme === 'github-light' ? 'default' : 'dark',
  });
  await mermaid.run({ nodes, suppressErrors: true });
  for (const node of nodes) {
    prepareDiagram(node);
    const svg = node.querySelector('svg');
    if (svg) matchEdgeLabelBackgrounds(svg, node);
  }
  layoutDiagrams(container);
}

// ---------------------------------------------------------------------------
// Edge labels on coloured containers
// ---------------------------------------------------------------------------

// Filled regions an edge label can sit on: flowchart/class clusters, state composites
// (rect.outer spans the whole composite including its title strip, rect.inner is the
// body below it and is filled differently) and state concurrency dividers.
const CONTAINER_RECTS = '.cluster rect, .statediagram-cluster rect, rect.divider';
const MIN_TEXT_CONTRAST = 4.5;
const NEAR_WHITE = { r: 245, g: 245, b: 245, a: 1 };
const NEAR_BLACK = { r: 26, g: 26, b: 26, a: 1 };

function parseColor(value) {
  const match = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/.exec(value.trim());
  if (!match) return null;
  let alpha = 1;
  if (match[4] !== undefined) alpha = match[4].endsWith('%') ? parseFloat(match[4]) / 100 : parseFloat(match[4]);
  return { r: +match[1], g: +match[2], b: +match[3], a: alpha };
}

function numberOr(value, fallback) {
  const number = parseFloat(value);
  return Number.isNaN(number) ? fallback : number;
}

function over(top, base) {
  const a = top.a;
  return {
    r: top.r * a + base.r * (1 - a),
    g: top.g * a + base.g * (1 - a),
    b: top.b * a + base.b * (1 - a),
    a: 1,
  };
}

function luminance({ r, g, b }) {
  const [lr, lg, lb] = [r, g, b].map((channel) => {
    const c = channel / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// Sets --edge-label-bg on each edge label to the fill of the innermost container behind
// its centre, so the label reads as a gap in the line rather than a patch. Labels on the
// plain diagram surface keep the --code-bg fallback. Geometry is compared in viewport
// coordinates so group transforms and nesting do not matter; run once per rendered svg.
// Then makes sure the text painted on that background is readable (ensureLabelTextContrast).
function matchEdgeLabelBackgrounds(svg, pre) {
  const labels = svg.querySelectorAll('g.edgeLabel');
  if (labels.length === 0) return;
  const surface = parseColor(getComputedStyle(pre).backgroundColor);
  if (!surface) return;
  surface.a = 1;

  const containers = Array.from(svg.querySelectorAll(CONTAINER_RECTS), (rect) => {
    const box = rect.getBoundingClientRect();
    return { rect, box, area: box.width * box.height, parent: undefined, color: undefined };
  }).filter((container) => container.area > 0)
    .sort((a, b) => a.area - b.area); // innermost first

  const holds = (box, x, y) => x >= box.left && x <= box.right && y >= box.top && y <= box.bottom;

  // Opaque colour of a container: its fill (with fill-opacity and opacity) over the
  // next larger container that contains it, ending at the diagram surface.
  const resolve = (index) => {
    const container = containers[index];
    if (container.color !== undefined) return container.color;
    const style = getComputedStyle(container.rect);
    const fill = parseColor(style.fill);
    let color = null;
    if (fill) {
      const alpha = fill.a * numberOr(style.fillOpacity, 1) * numberOr(style.opacity, 1);
      const cx = container.box.left + container.box.width / 2;
      const cy = container.box.top + container.box.height / 2;
      let base = surface;
      for (let i = index + 1; i < containers.length; i++) {
        if (containers[i].area > container.area && holds(containers[i].box, cx, cy)) {
          base = resolve(i);
          if (base) break;
        }
      }
      color = base ? over({ ...fill, a: alpha }, base) : null;
    }
    container.color = color;
    return color;
  };

  const pageText = pageTextColor(pre);
  for (const label of labels) {
    const box = label.getBoundingClientRect();
    if (box.width === 0 || box.height === 0) continue;
    const x = box.left + box.width / 2;
    const y = box.top + box.height / 2;
    const index = containers.findIndex((container) => holds(container.box, x, y));
    let background = surface;
    if (index !== -1) {
      const color = resolve(index);
      if (color) {
        background = color;
        label.style.setProperty('--edge-label-bg', cssRgb(color));
      }
    }
    ensureLabelTextContrast(label, background, pageText);
  }
}

function cssRgb({ r, g, b }) {
  return `rgb(${Math.round(r)}, ${Math.round(g)}, ${Math.round(b)})`;
}

// The page's --text colour, resolved by the browser (it may be any CSS colour syntax).
function pageTextColor(pre) {
  const probe = document.createElement('span');
  probe.style.color = 'var(--text)';
  pre.appendChild(probe);
  const color = parseColor(getComputedStyle(probe).color);
  probe.remove();
  return color && { ...color, a: 1 };
}

// The element that paints the label's glyphs: the parent of its first non-blank text node.
// Page rules such as `#rendered p` reach into the svg, so this can differ from the
// label's outer span.
function glyphElement(label) {
  const walker = document.createTreeWalker(label, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeValue.trim()) return node.parentElement;
  }
  return null;
}

// If the label's effective text colour (opacity of the glyph element and its ancestors up
// to the label included) is below MIN_TEXT_CONTRAST on its background, overrides it with
// the page text colour, near-white or near-black, whichever contrasts most. Passing
// labels keep their colour.
function ensureLabelTextContrast(label, background, pageText) {
  const element = glyphElement(label);
  if (!element) return;
  const style = getComputedStyle(element);
  const text = parseColor(element instanceof SVGElement ? style.fill : style.color);
  if (!text) return;
  let opacity = 1;
  for (let node = element; node; node = node.parentElement) {
    opacity *= numberOr(getComputedStyle(node).opacity, 1);
    if (node === label) break;
  }
  const effective = over({ ...text, a: text.a * opacity }, background);
  if (contrast(effective, background) >= MIN_TEXT_CONTRAST) return;
  const best = [pageText, NEAR_WHITE, NEAR_BLACK]
    .filter(Boolean)
    .reduce((a, b) => (contrast(b, background) > contrast(a, background) ? b : a));
  label.style.setProperty('--edge-label-text', cssRgb(best));
  label.dataset.labelText = '';
}

// ---------------------------------------------------------------------------
// Diagram sizing: scaled to the column by default, expandable on request
// ---------------------------------------------------------------------------

// Natural (unscaled) width of a rendered Mermaid svg. Mermaid's useMaxWidth sets
// an inline max-width in px; otherwise fall back to the viewBox, then the width attribute.
function naturalSvgWidth(svg) {
  const maxWidth = parseFloat(svg.style.maxWidth);
  if (maxWidth > 0 && svg.style.maxWidth.endsWith('px')) return maxWidth;
  const viewBox = svg.viewBox?.baseVal;
  if (viewBox && viewBox.width > 0) return viewBox.width;
  const attr = svg.getAttribute('width') || '';
  if (/^[\d.]+(px)?$/.test(attr) && parseFloat(attr) > 0) return parseFloat(attr);
  return 0;
}

// Wraps the svg in a horizontal scroll area inside the pre, records its natural width
// and adds the Expand button. The wrapper is inert until the diagram is expanded.
function prepareDiagram(pre) {
  const svg = pre.querySelector(':scope > svg');
  if (!svg) return;
  const width = naturalSvgWidth(svg);
  if (!width) return;
  const scroller = document.createElement('div');
  scroller.className = 'mermaid-scroll';
  svg.replaceWith(scroller);
  scroller.appendChild(svg);
  pre.dataset.naturalWidth = String(width);
  pre.dataset.svgMaxWidth = svg.style.maxWidth;

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'diagram-expand';
  button.hidden = true; // shown by layoutDiagrams when the diagram is being scaled down
  button.addEventListener('click', () => toggleDiagram(pre, button));
  pre.appendChild(button);
  applyDiagramState(pre);
}

function dropEmptyStyle(element) {
  if (element.getAttribute('style') === '') element.removeAttribute('style');
}

function isExpanded(pre) {
  return expandedDiagrams.has(pre.dataset.diagramKey);
}

// Expanded: natural size, never scaled below it; the scroll area handles anything wider
// than the room available. Collapsed: Mermaid's own scale-to-fit sizing, no inline overrides.
function applyDiagramState(pre) {
  const svg = pre.querySelector('svg');
  const button = pre.querySelector(':scope > .diagram-expand');
  const expanded = isExpanded(pre);
  pre.classList.toggle('is-expanded', expanded);
  if (expanded) {
    svg.style.width = `${pre.dataset.naturalWidth}px`;
    svg.style.maxWidth = 'none';
  } else {
    svg.style.width = '';
    svg.style.maxWidth = pre.dataset.svgMaxWidth;
  }
  dropEmptyStyle(svg);
  button.textContent = expanded ? 'Collapse' : 'Expand';
  button.setAttribute('aria-label', expanded ? 'Collapse diagram' : 'Expand diagram');
  button.setAttribute('aria-expanded', String(expanded));
}

function toggleDiagram(pre, button) {
  const container = pre.closest('#rendered');
  const scrollArea = container.closest('.content');
  const top = pre.getBoundingClientRect().top;
  const key = pre.dataset.diagramKey;
  if (expandedDiagrams.has(key)) expandedDiagrams.delete(key);
  else expandedDiagrams.add(key);
  applyDiagramState(pre);
  layoutDiagrams(container);
  // Keep the diagram's top edge where it was; it only grows downwards.
  scrollArea.scrollTop += pre.getBoundingClientRect().top - top;
  button.focus({ preventScroll: true });
  container.dispatchEvent(new CustomEvent('diagramlayout'));
}

// Room kept free beside an expanded diagram for the block comment markers and their focus ring.
const DIAGRAM_GUTTER = 40;

// Shows Expand on diagrams that are being scaled down, grows expanded diagrams beyond the
// text column, centred on the column, up to the scroll area width minus a gutter each
// side. Anything still wider scrolls.
export function layoutDiagrams(container) {
  const scrollArea = container.closest('.content');
  const diagrams = Array.from(container.querySelectorAll('pre.mermaid[data-natural-width]'));
  if (!scrollArea || diagrams.length === 0) return;

  for (const pre of diagrams) {
    pre.style.width = '';
    pre.style.marginLeft = '';
    dropEmptyStyle(pre);
  }
  const available = scrollArea.clientWidth - DIAGRAM_GUTTER * 2;
  const containerRect = container.getBoundingClientRect();
  const column = container.clientWidth;
  const columnLeft = containerRect.left + container.clientLeft;
  const measured = diagrams.map((pre) => {
    const rect = pre.getBoundingClientRect();
    const style = getComputedStyle(pre);
    const padding = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight);
    return { pre, inset: rect.left - columnLeft, box: rect.width, wanted: Number(pre.dataset.naturalWidth) + padding };
  });
  for (const { pre, inset, box, wanted } of measured) {
    const scaledDown = wanted > box + 1;
    const expanded = isExpanded(pre);
    pre.querySelector(':scope > .diagram-expand').hidden = !scaledDown;
    if (scaledDown && expanded) {
      const width = Math.max(box, Math.min(wanted, available));
      pre.style.width = `${width}px`;
      pre.style.marginLeft = `${(column - width) / 2 - inset}px`;
    }
    dropEmptyStyle(pre);
    const scroller = pre.querySelector(':scope > .mermaid-scroll');
    const overflows = expanded && scroller.scrollWidth > scroller.clientWidth + 1;
    if (overflows) {
      scroller.tabIndex = 0;
      scroller.setAttribute('role', 'region');
      scroller.setAttribute('aria-label', 'Diagram, scrollable');
    } else {
      scroller.removeAttribute('tabindex');
      scroller.removeAttribute('role');
      scroller.removeAttribute('aria-label');
    }
  }
}

export function setTheme(themeName) {
  currentTheme = themeName;
}

export function getTheme() {
  return currentTheme;
}
