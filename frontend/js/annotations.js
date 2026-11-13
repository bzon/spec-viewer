import { buildFeedback } from './annotation-feedback.js';
import { apiUrl, workspaceId } from './workspace.js';

const BLOCK_SELECTOR = 'p, li, h1, h2, h3, h4, td, th';
const CONTEXT_LENGTH = 64;
const BLOCK_QUOTE_LIMIT = 500;
const MAX_BLOCK_COUNT = 50;
const MAX_LANES = 4;
const LANE_STEP = 10;
const MARKER_CLEARANCE = 12;
const TEXT_QUOTE_LIMIT = 3000;
const DIAGRAM_OR_CODE = 'pre.mermaid, .code-block-wrapper';
// Rendered output and UI that is not document text: never part of any quote or context.
// Hover controls inside code blocks and diagrams; never part of a block's text.
const BLOCK_CONTROLS = '.copy-btn, .annotation-block-add, .diagram-expand';
const NON_TEXT = `svg, style, ${BLOCK_CONTROLS}, .annotation-block-marker`;

const DELIVERY_FIELDS = ['sent_at', 'read_at', 'read_by'];

let root;
let toggle;
let status;
let commentsList;
let commentsEmpty;
let feedbackButton;
let clearButton;
let active = false;
let suspended = false; // the folder is gone; nothing can be created or saved
let currentFile = null;
let loadState = 'loading'; // 'loading' | 'ready' | 'failed' for currentFile
let reopenRequest = null; // thread that was open when a live reload began
let scrollFrame = 0;
let annotations = [];
let popover = null;
let popoverState = null;
let fab = null;
let fabRange = null;
let fabTimer = null;
let statusTimer = null;
let pointerDown = false;
let saveQueue = Promise.resolve();
let layoutFrame = 0;

// Runtime-only state, never sent to the server.
const drafts = new Set(); // annotation ids created by "Comment" but not yet given text
const anchors = new Map(); // id -> { nodes, focusEl, detached }
const replyDrafts = new Map(); // annotation id -> unsent reply text for the current document
let blockMarkers = []; // { id, el, first, last }

export function initAnnotations(options) {
  root = options.root;
  toggle = options.toggle;
  status = options.status;
  commentsList = options.commentsList;
  commentsEmpty = options.commentsEmpty;
  feedbackButton = options.feedbackButton;

  toggle.addEventListener('click', () => setActive(!active));
  feedbackButton.addEventListener('click', copyFeedback);
  clearButton = document.getElementById('clear-annotations');
  clearButton.addEventListener('click', clearAllAnnotations);
  fab = createFab();

  // On document so a drag that ends outside #rendered still shows the button.
  document.addEventListener('mouseup', () => {
    if (active) setTimeout(highlightSelection, 0);
    else setTimeout(updateFab, 0);
  });
  root.addEventListener('click', (event) => {
    const target = event.target.closest('.annotation-highlight, .annotation-block-marker');
    if (target) openPopover(target.dataset.annotationId, target);
  });
  root.addEventListener('keydown', (event) => {
    const mark = event.target.closest('.annotation-highlight');
    if (mark && (event.key === 'Enter' || event.key === ' ')) {
      event.preventDefault();
      openPopover(mark.dataset.annotationId, mark);
    }
  });

  document.addEventListener('mousedown', (event) => {
    pointerDown = true;
    reopenRequest = null;
    if (popover && !popover.contains(event.target) && !event.target.closest('.annotation-highlight, .annotation-block-marker')) {
      closePopover();
    }
  });
  document.addEventListener('mouseup', () => { pointerDown = false; });
  document.addEventListener('selectionchange', () => {
    if (document.activeElement === fab) return;
    hideFab();
    clearTimeout(fabTimer);
    if (!pointerDown && !active) fabTimer = setTimeout(updateFab, 250);
  });
  document.addEventListener('scroll', hideFab, true);
  window.addEventListener('resize', () => {
    hideFab();
    if (popover) placePopover();
    scheduleLayout();
  });
  if (typeof ResizeObserver === 'function') new ResizeObserver(scheduleLayout).observe(root);

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      reopenRequest = null;
      if (popover) closePopover({ restoreFocus: true });
      else if (fab && !fab.hidden) hideFab();
      else if (active) setActive(false);
      return;
    }
    if (event.metaKey || event.ctrlKey || event.altKey || isEditable(event.target)) return;
    const key = event.key.toLowerCase();
    if (key === 'h') {
      event.preventDefault();
      setActive(!active);
    } else if (key === 'c') {
      const range = selectionRange();
      if (range) {
        event.preventDefault();
        commentOnRange(range);
      }
    }
  });

  renderPanel();
}

// Synchronous switch to a document whose HTML is about to replace the
// rendered one. Closes the popover while the old file is still current (so
// typed text is saved to the right document), then clears per-document state.
// Nothing can be created or saved until loadAnnotations succeeds.
export function beginDocument(filePath) {
  const sameFile = filePath === currentFile;
  reopenRequest = null;
  if (sameFile && popoverState && annotations.some((item) => item.id === popoverState.id)) {
    reopenRequest = { id: popoverState.id, focusReply: document.activeElement === popoverState.replyInput };
  }
  closePopover();
  hideFab();
  if (!sameFile) replyDrafts.clear();
  currentFile = filePath;
  annotations = [];
  drafts.clear();
  anchors.clear();
  blockMarkers = [];
  loadState = 'loading';
  renderPanel();
}

// The document is no longer available: drop its comments and disable every control.
export function suspendAnnotations() {
  if (active) setActive(false);
  suspended = true;
  closePopover({ save: false });
  hideFab();
  currentFile = null;
  annotations = [];
  drafts.clear();
  anchors.clear();
  replyDrafts.clear();
  blockMarkers = [];
  loadState = 'failed';
  setStatus('');
  renderPanel();
}

export async function loadAnnotations(filePath) {
  if (filePath !== currentFile) beginDocument(filePath);

  try {
    await settledSaves();
    if (currentFile !== filePath) return;
    const loaded = await fetchAnnotations(filePath);
    if (currentFile !== filePath) return;
    annotations = loaded.items.map(normalizeAnnotation);
    loadState = 'ready';
    applyAnnotations();
    setStatus('');
    reopenAfterReload();
  } catch {
    if (currentFile === filePath) {
      loadState = 'failed';
      setStatus('Could not load comments');
    }
  }
}

// A live reload closes the popover; bring the same thread back if it survived.
function reopenAfterReload() {
  const request = reopenRequest;
  reopenRequest = null;
  if (!request || popover || !annotations.some((item) => item.id === request.id)) return;
  openPopover(request.id, null);
  if (request.focusReply) popoverState?.replyInput.focus({ preventScroll: true });
}

// Creating or clearing annotations before the stored list is known could
// overwrite the document's other comments with a partial list.
function annotationsReady() {
  if (loadState === 'ready') return true;
  setStatus(loadState === 'loading' ? 'Loading comments\u2026 try again in a moment' : 'Comments could not be loaded; reload the page to add comments');
  return false;
}

// Re-reads server data (replies, authors) without touching the document DOM
// and without disturbing anything typed into an open popover.
export async function refreshAnnotations(filePath) {
  if (filePath !== currentFile) return;
  try {
    await settledSaves();
    if (currentFile !== filePath) return;
    const loaded = await fetchAnnotations(filePath);
    if (currentFile !== filePath) return;
    mergeServerData(loaded.items);
  } catch {
    /* keep what is shown; the next event or reload will retry */
  }
}

async function fetchAnnotations(filePath) {
  const response = await fetch(apiUrl('/api/annotations', { path: filePath }));
  if (!response.ok) throw new Error(response.statusText);
  return { items: await response.json() };
}

// Waits for queued saves so a read cannot overtake a pending write.
async function settledSaves() {
  try {
    await saveQueue;
  } catch {
    /* save failures are reported by the save itself */
  }
}

function normalizeAnnotation(item) {
  return {
    ...item,
    prefix: item.prefix ?? '',
    suffix: item.suffix ?? '',
    note: item.note ?? '',
    author: item.author ?? '',
    replies: Array.isArray(item.replies) ? item.replies : [],
  };
}

function mergeServerData(loaded) {
  const byId = new Map(loaded.map((item) => [item.id, item]));
  for (const annotation of annotations) {
    const server = byId.get(annotation.id);
    if (!server) continue;
    annotation.author = server.author ?? annotation.author;
    annotation.replies = Array.isArray(server.replies) ? server.replies : [];
    // Delivery marks are server-owned: take them as they are, including their absence.
    for (const field of DELIVERY_FIELDS) {
      if (server[field]) annotation[field] = server[field];
      else delete annotation[field];
    }
    refreshMarkers(annotation);
  }
  renderPanel();
  renderOpenThread();
}

function setActive(next) {
  if (suspended) return;
  active = next;
  toggle.setAttribute('aria-pressed', String(active));
  toggle.classList.toggle('active', active);
  root.classList.toggle('highlight-mode', active);
  if (active) setStatus('Highlighter on');
  else setStatus('Highlighter off', 1200);
  hideFab();
  if (!active) window.getSelection()?.removeAllRanges();
}

// ---------------------------------------------------------------------------
// Creating annotations
// ---------------------------------------------------------------------------

function highlightSelection() {
  const selection = window.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return;

  const range = selection.getRangeAt(0);
  if (!root.contains(range.commonAncestorContainer)) return;
  const target = textTarget(range);
  if (target.reason) {
    if (target.reason !== 'empty') setStatus(reasonMessage(target.reason));
    selection.removeAllRanges();
    return;
  }

  if (!annotationsReady()) {
    selection.removeAllRanges();
    return;
  }
  const annotation = createTextAnnotation(range, target.block);
  selection.removeAllRanges();
  renderPanel();
  saveAnnotations();
  return annotation;
}

function reasonMessage(reason) {
  if (reason === 'highlight') return 'That selection already contains a highlight';
  if (reason === 'long') return `Select at most ${TEXT_QUOTE_LIMIT} characters to comment on`;
  return 'Select text within one paragraph or list item';
}

function textTarget(range) {
  const startElement = elementFor(range.startContainer);
  const endElement = elementFor(range.endContainer);
  const block = startElement?.closest(BLOCK_SELECTOR);
  if (!block || block !== endElement?.closest(BLOCK_SELECTOR) || !root.contains(block) || startElement.closest('pre')) {
    return { reason: 'multi' };
  }
  if (range.cloneContents().querySelector?.('.annotation-highlight') || startElement.closest('.annotation-highlight') || endElement.closest('.annotation-highlight')) {
    return { reason: 'highlight' };
  }
  const { quote } = textSlice(block, range);
  if (!quote.trim()) return { reason: 'empty' };
  if (quote.length > TEXT_QUOTE_LIMIT) return { reason: 'long' };
  return { block };
}

function createTextAnnotation(range, block) {
  const { quote, start, text: blockText } = textSlice(block, range);
  const annotation = newAnnotation({
    quote,
    prefix: blockText.slice(Math.max(0, start - CONTEXT_LENGTH), start),
    suffix: blockText.slice(start + quote.length, start + quote.length + CONTEXT_LENGTH),
  });
  annotations.push(annotation);
  applyAnnotation(annotation);
  return annotation;
}

// first and last are the covered blocks: top-level, or one nested diagram/code block (count 1).
function createBlockAnnotation(first, last, count) {
  const annotation = newAnnotation({
    quote: blockIdentity(first),
    prefix: contextBefore(first),
    suffix: contextAfter(last),
    scope: 'block',
    block_count: count,
  });
  annotations.push(annotation);
  applyAnnotation(annotation);
  return annotation;
}

function newAnnotation(fields) {
  const now = new Date().toISOString();
  return {
    id: crypto.randomUUID(),
    note: '',
    author: '',
    replies: [],
    created_at: now,
    updated_at: now,
    ...fields,
  };
}

function commentOnRange(range) {
  if (!annotationsReady()) return;
  const target = textTarget(range);
  let annotation;
  if (target.reason === 'long') {
    setStatus(reasonMessage('long'));
    return;
  }
  if (target.reason === 'highlight') {
    const mark = elementFor(range.startContainer)?.closest('.annotation-highlight') || elementFor(range.endContainer)?.closest('.annotation-highlight');
    if (mark) {
      window.getSelection()?.removeAllRanges();
      hideFab();
      openPopover(mark.dataset.annotationId, mark);
      return;
    }
  }
  if (!target.reason) {
    annotation = createTextAnnotation(range, target.block);
  } else if (nestedBlockAt(range)) {
    const nested = nestedBlockAt(range);
    annotation = createBlockAnnotation(nested, nested, 1);
  } else {
    const span = blockSpan(range);
    if (!span) {
      setStatus('Select text inside the document to comment');
      return;
    }
    if (span.count > MAX_BLOCK_COUNT) {
      setStatus(`Select at most ${MAX_BLOCK_COUNT} blocks`);
      return;
    }
    const blocks = topBlocks();
    annotation = createBlockAnnotation(blocks[span.index], blocks[span.index + span.count - 1], span.count);
  }
  startDraft(annotation);
}

function commentOnBlock(block) {
  if (!annotationsReady()) return;
  if (!root.contains(block)) return;
  startDraft(createBlockAnnotation(block, block, 1));
}

function startDraft(annotation) {
  drafts.add(annotation.id);
  window.getSelection()?.removeAllRanges();
  hideFab();
  layoutMarkers();
  openPopover(annotation.id, null);
}

// Top-level blocks (direct children of #rendered) touched by the range.
function blockSpan(range) {
  const blocks = topBlocks();
  const first = topBlockAt(range.startContainer, range.startOffset, false);
  const last = topBlockAt(range.endContainer, range.endOffset, true);
  if (!first || !last) return null;
  let firstIndex = blocks.indexOf(first);
  let lastIndex = blocks.indexOf(last);
  if (firstIndex === -1 || lastIndex < firstIndex) return null;

  if (lastIndex > firstIndex) {
    // A drag that ends at the very start of the next block does not touch it.
    const tail = document.createRange();
    tail.setStart(blocks[lastIndex], 0);
    tail.setEnd(range.endContainer, range.endOffset);
    if (!tail.toString().trim()) lastIndex--;
  }
  if (lastIndex > firstIndex) {
    const head = document.createRange();
    head.setStart(range.startContainer, range.startOffset);
    head.setEnd(blocks[firstIndex], blocks[firstIndex].childNodes.length);
    if (!head.toString().trim()) firstIndex++;
  }
  return { index: firstIndex, count: lastIndex - firstIndex + 1 };
}

// The single diagram or code block that contains the whole range, when it is nested
// (not a direct child of #rendered). Top-level ones are covered by the span logic.
function nestedBlockAt(range) {
  const start = elementFor(range.startContainer)?.closest(DIAGRAM_OR_CODE);
  const end = elementFor(range.endContainer)?.closest(DIAGRAM_OR_CODE);
  return start && start === end && start.parentElement !== root && root.contains(start) ? start : null;
}

// Every diagram and code block below #rendered that is not a direct child of it.
function nestedBlocks() {
  return Array.from(root.querySelectorAll(DIAGRAM_OR_CODE)).filter((element) => element.parentElement !== root);
}

function topBlocks() {
  return Array.from(root.children).filter((element) => !isBlockMarker(element));
}

function topBlockAt(node, offset, isEnd) {
  let element = node;
  if (node === root) {
    const children = root.childNodes;
    let index = isEnd ? offset - 1 : offset;
    const step = isEnd ? -1 : 1;
    while (index >= 0 && index < children.length && (children[index].nodeType !== Node.ELEMENT_NODE || isBlockMarker(children[index]))) index += step;
    element = children[index];
  }
  while (element && element.parentNode !== root) element = element.parentNode;
  return element && isBlockMarker(element) ? null : element ?? null;
}

// Block markers sit directly before their block, so they are tab stops in
// document order. Top-level blocks have them as children of #rendered, nested
// ones inside the container (e.g. the li) of the block. They are never document blocks.
function isBlockMarker(element) {
  return element.classList?.contains('annotation-block-marker') ?? false;
}

// Text of a node as the author wrote it: a rendered diagram counts as its source,
// and SVG output, styles, the copy/comment/expand buttons and block markers count as nothing.
// Unlike textContent this does not change when Mermaid renders or a button label changes.
function stableText(node) {
  if (node.nodeType === Node.TEXT_NODE) return node.data;
  if (node.nodeType !== Node.ELEMENT_NODE || node.matches(NON_TEXT)) return '';
  if (node.matches('pre.mermaid') && node.dataset.source !== undefined) return node.dataset.source;
  let text = '';
  for (const child of node.childNodes) text += stableText(child);
  return text;
}

// Text that identifies a block (top-level or nested), independent of how it is rendered.
function blockIdentity(block) {
  const text = stableText(block).trim().slice(0, BLOCK_QUOTE_LIMIT);
  return text || `[${block.tagName.toLowerCase()}]`;
}

function nextBlockSibling(element, step) {
  let sibling = step < 0 ? element.previousElementSibling : element.nextElementSibling;
  while (sibling && isBlockMarker(sibling)) sibling = step < 0 ? sibling.previousElementSibling : sibling.nextElementSibling;
  return sibling;
}

// Text before/after an element that has no element sibling on that side:
// the container's own loose text (e.g. a list item's text before a diagram).
function looseText(element, step) {
  let text = '';
  for (let node = step < 0 ? element.previousSibling : element.nextSibling; node; node = step < 0 ? node.previousSibling : node.nextSibling) {
    text = step < 0 ? stableText(node) + text : text + stableText(node);
  }
  return text.trim();
}

// Tail of the nearest meaningful text before a block: the previous sibling block,
// else the container's own text before it, else what precedes the container.
function contextBefore(block) {
  for (let current = block; current && current !== root; current = current.parentElement) {
    const sibling = nextBlockSibling(current, -1);
    if (sibling) return blockIdentity(sibling).slice(-CONTEXT_LENGTH);
    const loose = looseText(current, -1);
    if (loose) return loose.slice(-CONTEXT_LENGTH);
  }
  return '';
}

function contextAfter(block) {
  for (let current = block; current && current !== root; current = current.parentElement) {
    const sibling = nextBlockSibling(current, 1);
    if (sibling) return blockIdentity(sibling).slice(0, CONTEXT_LENGTH);
    const loose = looseText(current, 1);
    if (loose) return loose.slice(0, CONTEXT_LENGTH);
  }
  return '';
}

// Text nodes of a block that count as its text for highlights: the same exclusions as
// stableText, and diagrams entirely (a rendered diagram has no source text to wrap).
function textNodesOf(block) {
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode: (node) => {
      if (node.nodeType === Node.TEXT_NODE) return NodeFilter.FILTER_ACCEPT;
      return node.matches(`${NON_TEXT}, pre.mermaid`) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_SKIP;
    },
  });
  const nodes = [];
  let node;
  while ((node = walker.nextNode())) nodes.push(node);
  return nodes;
}

// Block text, the selected part of it and where that starts, all measured on textNodesOf.
function textSlice(block, range) {
  let text = '';
  let quote = '';
  let start = null;
  for (const node of textNodesOf(block)) {
    const length = node.data.length;
    if (range.comparePoint(node, length) >= 0 && range.comparePoint(node, 0) <= 0) {
      const from = node === range.startContainer ? range.startOffset : 0;
      const to = node === range.endContainer ? range.endOffset : length;
      if (start === null) start = text.length + from;
      quote += node.data.slice(from, to);
    }
    text += node.data;
  }
  return { text, quote, start: start ?? 0 };
}

// ---------------------------------------------------------------------------
// Anchoring
// ---------------------------------------------------------------------------

function applyAnnotations() {
  anchors.clear();
  blockMarkers = [];
  root.querySelectorAll('.annotation-block-marker').forEach((marker) => marker.remove());
  root.querySelectorAll('.annotation-highlight').forEach(unwrapMark);
  for (const annotation of annotations) applyAnnotation(annotation);
  ensureBlockCommentButtons();
  layoutMarkers();
  renderPanel();
}

function applyAnnotation(annotation) {
  const found = annotation.scope === 'block' ? applyBlockAnnotation(annotation) : applyTextAnnotation(annotation);
  if (!found) anchors.set(annotation.id, { nodes: [], focusEl: null, detached: true });
}

function applyTextAnnotation(annotation) {
  let best = null;
  for (const block of root.querySelectorAll(BLOCK_SELECTOR)) {
    const nodes = textNodesOf(block);
    const text = nodes.map((node) => node.data).join('');
    let index = text.indexOf(annotation.quote);
    while (index !== -1) {
      if (!overlapsHighlight(nodes, index, index + annotation.quote.length)) {
        const score = contextScore(text, index, annotation);
        if (!best || score > best.score) best = { block, index, score };
      }
      index = text.indexOf(annotation.quote, index + 1);
    }
  }
  if (!best) return false;

  const marks = wrapText(textNodesOf(best.block), best.index, best.index + annotation.quote.length, annotation.id);
  for (const mark of marks) updateMark(mark, annotation);
  marks.reverse();
  anchors.set(annotation.id, { nodes: marks, focusEl: marks[0], detached: false });
  return true;
}

function applyBlockAnnotation(annotation) {
  const blocks = topBlocks();
  const count = annotation.block_count || 1;
  // Top-level candidates come first so that a tie in score keeps the top-level block.
  const candidates = blocks.map((block, index) => ({ first: block, last: blocks[index + count - 1] }));
  if (count === 1) for (const block of nestedBlocks()) candidates.push({ first: block, last: block });
  let best = null;
  for (const candidate of candidates) {
    if (!candidate.last || blockIdentity(candidate.first) !== annotation.quote) continue;
    const score = matchingSuffix(contextBefore(candidate.first), annotation.prefix) + matchingPrefix(contextAfter(candidate.last), annotation.suffix);
    if (!best || score > best.score) best = { ...candidate, score };
  }
  if (!best) return false;

  const covered = [best.first];
  if (best.last !== best.first) for (let block = nextBlockSibling(best.first, 1); block; block = nextBlockSibling(block, 1)) {
    covered.push(block);
    if (block === best.last) break;
  }
  const marker = document.createElement('button');
  marker.type = 'button';
  marker.className = 'annotation-block-marker';
  marker.dataset.annotationId = annotation.id;
  best.first.before(marker);
  blockMarkers.push({ id: annotation.id, el: marker, first: best.first, last: best.last });
  anchors.set(annotation.id, { nodes: covered, focusEl: marker, detached: false });
  updateBlockMarker(marker, annotation);
  return true;
}

function scheduleLayout() {
  if (layoutFrame) return;
  layoutFrame = requestAnimationFrame(() => {
    layoutFrame = 0;
    layoutMarkers();
  });
}

export function layoutAnnotationMarkers() {
  layoutMarkers();
}

function layoutMarkers() {
  // Measured against #rendered (the markers' containing block), not offsetTop,
  // because nested blocks have other offset parents.
  const rootRect = root.getBoundingClientRect();
  const origin = rootRect.top + root.clientTop;
  const originLeft = rootRect.left + root.clientLeft;
  const rows = blockMarkers
    .filter((item) => item.first.isConnected && item.last.isConnected)
    .map((item) => ({
      item,
      top: item.first.getBoundingClientRect().top - origin,
      bottom: item.last.getBoundingClientRect().bottom - origin,
      // Diagrams can break out of the column; keep the marker left of the widest one.
      edge: Math.min(0, ...(anchors.get(item.id)?.nodes ?? [item.first]).map((node) => node.getBoundingClientRect().left - originLeft)),
    }))
    .sort((a, b) => a.top - b.top);
  // Never closer than MARKER_CLEARANCE to the scroll area's left edge (it clips overflow,
  // and the focus ring needs room). Lanes that would cross it overlay the block's own
  // left padding instead of being clipped.
  const scrollArea = root.closest('.content');
  const minLeft = scrollArea ? scrollArea.getBoundingClientRect().left + scrollArea.clientLeft - originLeft + MARKER_CLEARANCE : -Infinity;
  const laneBottoms = [];
  for (const row of rows) {
    let lane = laneBottoms.findIndex((bottom) => bottom <= row.top);
    if (lane === -1) lane = laneBottoms.length;
    laneBottoms[lane] = row.bottom;
    row.item.el.style.top = `${row.top}px`;
    row.item.el.style.height = `${Math.max(row.bottom - row.top, 8)}px`;
    const lanesOutside = Math.max(1, Math.floor((row.edge - 18 - minLeft) / LANE_STEP) + 1);
    const overlaysPadding = row.item.first.matches?.('pre.mermaid') && lane >= lanesOutside;
    const left = !overlaysPadding
      ? row.edge - 18 - Math.min(lane, MAX_LANES) * LANE_STEP
      : row.edge + 4 + Math.min(lane - lanesOutside, MAX_LANES) * LANE_STEP;
    row.item.el.style.left = `${left}px`;
  }
  if (popover) placePopover();
}

// Quiet "Comment" button inside code blocks and diagrams, shown on hover/focus.
function ensureBlockCommentButtons() {
  for (const block of root.querySelectorAll(DIAGRAM_OR_CODE)) {
    const isCode = block.classList.contains('code-block-wrapper');
    if (block.querySelector(':scope > .annotation-block-add')) continue;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'annotation-block-add';
    button.textContent = 'Comment';
    button.setAttribute('aria-label', isCode ? 'Comment on code block' : 'Comment on diagram');
    button.addEventListener('click', () => commentOnBlock(block));
    block.appendChild(button);
  }
}

function contextScore(text, index, annotation) {
  const actualPrefix = text.slice(Math.max(0, index - annotation.prefix.length), index);
  const actualSuffix = text.slice(index + annotation.quote.length, index + annotation.quote.length + annotation.suffix.length);
  return matchingSuffix(actualPrefix, annotation.prefix) + matchingPrefix(actualSuffix, annotation.suffix);
}

function matchingPrefix(a, b) {
  let count = 0;
  while (count < a.length && count < b.length && a[count] === b[count]) count++;
  return count;
}

function matchingSuffix(a, b) {
  let count = 0;
  while (count < a.length && count < b.length && a[a.length - 1 - count] === b[b.length - 1 - count]) count++;
  return count;
}

function overlapsHighlight(nodes, start, end) {
  let offset = 0;
  for (const node of nodes) {
    const nodeEnd = offset + node.data.length;
    if (nodeEnd > start && offset < end && node.parentElement.closest('.annotation-highlight')) return true;
    offset = nodeEnd;
  }
  return false;
}

function wrapText(nodes, start, end, id) {
  const matches = [];
  let offset = 0;
  for (const node of nodes) {
    const nodeStart = offset;
    const nodeEnd = offset + node.data.length;
    if (nodeEnd > start && nodeStart < end && !node.parentElement.closest('.annotation-highlight')) {
      matches.push({
        node,
        start: Math.max(0, start - nodeStart),
        end: Math.min(node.data.length, end - nodeStart),
      });
    }
    offset = nodeEnd;
  }

  const marks = [];
  for (const match of matches.reverse()) {
    const selected = match.node.splitText(match.start);
    selected.splitText(match.end - match.start);
    const mark = document.createElement('mark');
    mark.className = 'annotation-highlight';
    mark.dataset.annotationId = id;
    selected.replaceWith(mark);
    mark.appendChild(selected);
    marks.push(mark);
  }
  return marks;
}

function unwrapMark(mark) {
  const parent = mark.parentNode;
  if (!parent) return;
  mark.replaceWith(...mark.childNodes);
  parent.normalize();
}

function removeAnchor(id) {
  root.querySelectorAll(`.annotation-highlight[data-annotation-id="${CSS.escape(id)}"]`).forEach(unwrapMark);
  for (const item of blockMarkers.filter((marker) => marker.id === id)) item.el.remove();
  blockMarkers = blockMarkers.filter((marker) => marker.id !== id);
  anchors.delete(id);
  layoutMarkers();
}

function updateMark(mark, annotation) {
  const hasThread = hasComment(annotation);
  mark.classList.toggle('has-note', hasThread);
  mark.setAttribute('tabindex', '0');
  mark.setAttribute('role', 'button');
  mark.setAttribute('aria-label', hasThread ? 'Highlighted text with comment. Activate to open.' : 'Highlighted text. Activate to add a comment.');
}

function updateBlockMarker(marker, annotation) {
  const label = blockLabel(annotation);
  const text = shorten(annotation.note || annotation.replies[0]?.body || '', 60);
  marker.setAttribute('aria-label', text ? `Comment on ${label}: ${text}. Activate to open.` : `Comment on ${label}. Activate to open.`);
  marker.title = `Comment on ${label}`;
}

function refreshMarkers(annotation) {
  const block = blockMarkers.find((item) => item.id === annotation.id);
  if (block) updateBlockMarker(block.el, annotation);
  document.querySelectorAll(`.annotation-highlight[data-annotation-id="${CSS.escape(annotation.id)}"]`).forEach((mark) => updateMark(mark, annotation));
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

function saveAnnotations(message = '') {
  if (!currentFile) return saveQueue;
  if (loadState !== 'ready') {
    setStatus('Could not save: comments are still loading');
    return saveQueue;
  }
  const file = currentFile;
  const payload = JSON.stringify({ annotations: annotations.filter((item) => !drafts.has(item.id)).map(serializeAnnotation) });
  const run = async () => {
    try {
      const response = await fetch(apiUrl('/api/annotations', { path: file }), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: payload,
      });
      if (response.status === 400) {
        setStatus('Could not save: a comment is too long or invalid');
        return false;
      }
      if (!response.ok) throw new Error(response.statusText);
      if (message) setStatus(message, 2000);
      else setStatus(active ? 'Highlighter on' : 'Saved', active ? 0 : 1200);
      if (currentFile === file) {
        const loaded = await fetchAnnotations(file);
        if (currentFile === file) {
                mergeServerData(loaded.items);
        }
      }
      return true;
    } catch {
      setStatus('Could not save comments');
      return false;
    }
  };
  saveQueue = saveQueue.then(run, run);
  return saveQueue;
}

function serializeAnnotation(annotation) {
  const data = {
    id: annotation.id,
    quote: annotation.quote,
    prefix: annotation.prefix,
    suffix: annotation.suffix,
    note: annotation.note,
    author: annotation.author,
    created_at: annotation.created_at,
    updated_at: annotation.updated_at,
  };
  if (annotation.scope === 'block') {
    data.scope = 'block';
    data.block_count = annotation.block_count || 1;
  }
  return data;
}

async function postReply(annotation, body) {
  await saveQueue;
  if (drafts.has(annotation.id)) return false;
  const file = currentFile;
  try {
    const response = await fetch(apiUrl('/api/annotations/replies', { path: file }), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ annotation_id: annotation.id, author: '', body }),
    });
    if (!response.ok) throw new Error(response.statusText);
    const reply = await response.json();
    if (currentFile === file) {
      const current = annotations.find((item) => item.id === annotation.id);
      if (current && !current.replies.some((item) => item.id === reply.id)) current.replies.push(reply);
      if (current) clearDeliveryMarks(current); // a reply of mine makes the thread unsent again
      if (current) refreshMarkers(current);
      // The reply is sent: its text must not come back in a reopened popover.
      if (replyDrafts.get(annotation.id)?.trim() === body) replyDrafts.delete(annotation.id);
      if (popoverState?.id === annotation.id && popoverState.replyInput.value.trim() === body) popoverState.replyInput.value = '';
    }
    renderPanel();
    renderOpenThread({ scrollToEnd: true, onlyFor: annotation.id });
    refreshAnnotations(file);
    return true;
  } catch {
    setStatus('Could not send reply');
    return false;
  }
}

async function deleteReply(annotation, replyId) {
  const file = currentFile;
  const url = apiUrl('/api/annotations/replies', { path: file, annotation_id: annotation.id, reply_id: replyId });
  // Focus is on this reply's Delete button; once it is gone, hand focus to the
  // next reply's button, else the previous one's, else the reply box.
  let focusTarget = null;
  if (popoverState?.id === annotation.id && document.activeElement?.dataset.replyId === replyId && popoverState.replies.contains(document.activeElement)) {
    const index = annotation.replies.findIndex((item) => item.id === replyId);
    focusTarget = annotation.replies[index + 1]?.id ?? annotation.replies[index - 1]?.id ?? '';
  }
  try {
    const response = await fetch(url, { method: 'DELETE' });
    if (!response.ok && response.status !== 404) throw new Error(response.statusText);
    if (currentFile === file) {
      const current = annotations.find((item) => item.id === annotation.id);
      if (current) {
        current.replies = current.replies.filter((item) => item.id !== replyId);
        refreshMarkers(current);
      }
    }
    renderPanel();
    renderOpenThread();
    if (focusTarget !== null && popoverState?.id === annotation.id) {
      const buttons = Array.from(popoverState.replies.querySelectorAll('[data-reply-id]'));
      const next = buttons.find((el) => el.dataset.replyId === focusTarget);
      (next || popoverState.replyInput).focus({ preventScroll: true });
    }
  } catch {
    setStatus('Could not delete reply');
  }
}

function commitComment(annotation, text, message = '') {
  annotation.note = text;
  clearDeliveryMarks(annotation); // the server clears them when the note changes
  annotation.updated_at = new Date().toISOString();
  drafts.delete(annotation.id);
  refreshMarkers(annotation);
  renderPanel();
  if (message) setStatus(message, 2000);
  saveAnnotations(message);
}

function clearAllAnnotations() {
  if (!annotationsReady()) return;
  const saved = annotations.filter((item) => !drafts.has(item.id));
  if (saved.length === 0) return;
  const comments = saved.filter(hasComment).length;
  const highlights = saved.length - comments;
  const parts = [];
  if (highlights > 0) parts.push(highlights === 1 ? '1 highlight' : `${highlights} highlights`);
  if (comments > 0) parts.push(comments === 1 ? '1 comment' : `${comments} comments`);
  const everything = saved.length > 1 ? 'all ' : '';
  if (!window.confirm(`Remove ${everything}${parts.join(' and ')} from this document? Replies are removed too. This cannot be undone.`)) return;

  // Forget drafts first so closing the popover discards a typed draft instead of saving it.
  drafts.clear();
  replyDrafts.clear();
  closePopover({ save: false });
  for (const annotation of annotations) removeAnchor(annotation.id);
  annotations = [];
  renderPanel();
  const file = currentFile;
  saveAnnotations().then((saved) => {
    if (saved && currentFile === file) setStatus('Cleared highlights and comments', 2000);
  });
  toggle.focus();
}

function removeAnnotation(id) {
  const wasDraft = drafts.has(id);
  annotations = annotations.filter((annotation) => annotation.id !== id);
  drafts.delete(id);
  replyDrafts.delete(id);
  removeAnchor(id);
  renderPanel();
  closePopover({ save: false, restoreFocus: true });
  if (!wasDraft) saveAnnotations();
}

// ---------------------------------------------------------------------------
// Popover (thread)
// ---------------------------------------------------------------------------

function openPopover(id, opener) {
  if (popover && popoverState?.id === id) {
    (popoverState.editing ? popoverState.editor : popoverState.replyInput)?.focus();
    return;
  }
  if (!annotations.some((item) => item.id === id)) return;
  closePopover();
  // Closing may have discarded an empty draft.
  const annotation = annotations.find((item) => item.id === id);
  if (!annotation) return;

  const anchor = anchors.get(id);
  popover = document.createElement('div');
  popover.className = 'annotation-popover';
  popover.tabIndex = -1;
  popover.setAttribute('role', 'dialog');
  popover.setAttribute('aria-label', annotation.scope === 'block' ? 'Block comment thread' : 'Comment thread');

  const state = {
    id,
    opener,
    editing: !annotation.note,
    block: annotation.scope === 'block',
    anchorEl: anchor?.focusEl || opener,
    comment: document.createElement('div'),
    thread: document.createElement('div'),
    replies: document.createElement('ul'),
    replyForm: document.createElement('div'),
    editor: null,
    replyInput: null,
  };
  popoverState = state;

  const context = document.createElement('div');
  context.className = 'annotation-context';
  const quote = document.createElement('div');
  quote.className = 'annotation-quote';
  quote.textContent = quoteLine(annotation);
  context.appendChild(quote);
  if (anchor?.detached) {
    const detached = document.createElement('div');
    detached.className = 'annotation-detached';
    detached.textContent = 'Detached: this text can no longer be found in the document.';
    context.appendChild(detached);
  }

  state.comment.className = 'annotation-comment';
  state.replies.className = 'annotation-replies';
  buildReplyForm(annotation, state);

  const actions = document.createElement('div');
  actions.className = 'annotation-actions';
  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'annotation-remove';
  remove.textContent = 'Remove';
  remove.addEventListener('click', () => {
    if (window.confirm(annotation.scope === 'block' ? 'Remove this comment?' : 'Remove this highlight and its comments?')) removeAnnotation(id);
  });
  const close = document.createElement('button');
  close.type = 'button';
  close.textContent = 'Close';
  close.addEventListener('click', () => closePopover({ restoreFocus: true }));
  actions.append(remove, close);

  state.thread.className = 'annotation-thread';
  state.thread.append(context, state.comment, state.replies);
  popover.append(state.thread, state.replyForm, actions);
  document.body.appendChild(popover);
  renderComment(annotation, state);
  renderReplies(annotation, state);
  placePopover();
  document.addEventListener('scroll', onDocumentScroll, true);
  (state.editing ? state.editor : state.replyInput).focus({ preventScroll: true });
  state.thread.scrollTop = 0;
}

function renderComment(annotation, state) {
  state.comment.textContent = '';
  state.editor = null;
  if (state.editing) {
    const editor = document.createElement('textarea');
    editor.className = 'annotation-note';
    editor.rows = 3;
    editor.maxLength = 10000;
    editor.placeholder = 'Add a comment';
    editor.value = annotation.note || '';
    editor.setAttribute('aria-label', 'Comment');
    editor.addEventListener('keydown', submitOnModifierEnter(() => save.click()));
    state.editor = editor;

    const row = document.createElement('div');
    row.className = 'annotation-actions annotation-editor-actions';
    const save = document.createElement('button');
    save.type = 'button';
    save.className = 'annotation-save';
    save.textContent = annotation.note ? 'Save' : 'Comment';
    save.addEventListener('click', () => {
      const text = editor.value.trim();
      if (!text) {
        editor.focus();
        return;
      }
      commitComment(annotation, text);
      closePopover({ restoreFocus: true });
    });
    row.appendChild(save);
    if (annotation.note) {
      const cancel = document.createElement('button');
      cancel.type = 'button';
      cancel.textContent = 'Cancel';
      cancel.addEventListener('click', () => {
        state.editing = false;
        renderComment(annotation, state);
        state.replyInput.focus();
      });
      row.prepend(cancel);
    }
    state.comment.append(editor, row);
    syncReplyForm(annotation, state);
    return;
  }

  state.comment.append(entryHeader(annotation.author, annotation.created_at), bodyElement(annotation.note), deliveryLine(annotation));
  const edit = document.createElement('button');
  edit.type = 'button';
  edit.className = 'annotation-link';
  edit.textContent = 'Edit';
  edit.setAttribute('aria-label', 'Edit comment');
  edit.addEventListener('click', () => {
    state.editing = true;
    renderComment(annotation, state);
    state.editor.focus();
  });
  state.comment.querySelector('.annotation-entry-head').appendChild(edit);
  syncReplyForm(annotation, state);
}

function renderReplies(annotation, state) {
  const focusedReply = state.replies.contains(document.activeElement) ? document.activeElement.dataset.replyId : null;
  state.replies.textContent = '';
  state.replies.hidden = annotation.replies.length === 0;
  for (const reply of annotation.replies) {
    const item = document.createElement('li');
    item.className = 'annotation-reply';
    const header = entryHeader(reply.author, reply.created_at);
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'annotation-link';
    remove.textContent = 'Delete';
    remove.dataset.replyId = reply.id;
    remove.setAttribute('aria-label', `Delete reply by ${reply.author || 'unknown'}`);
    remove.addEventListener('click', () => {
      if (window.confirm('Delete this reply?')) deleteReply(annotation, reply.id);
    });
    header.appendChild(remove);
    item.append(header, bodyElement(reply.body));
    state.replies.appendChild(item);
  }
  if (focusedReply) {
    const target = Array.from(state.replies.querySelectorAll('[data-reply-id]')).find((el) => el.dataset.replyId === focusedReply);
    (target || state.replyInput)?.focus({ preventScroll: true });
  }
}

function buildReplyForm(annotation, state) {
  state.replyForm.className = 'annotation-reply-form';
  const input = document.createElement('textarea');
  input.className = 'annotation-note';
  input.rows = 2;
  input.maxLength = 10000;
  input.placeholder = 'Reply';
  input.setAttribute('aria-label', 'Reply');
  input.value = replyDrafts.get(annotation.id) ?? '';
  input.addEventListener('input', () => {
    if (input.value) replyDrafts.set(annotation.id, input.value);
    else replyDrafts.delete(annotation.id);
  });
  state.replyInput = input;

  const send = document.createElement('button');
  send.type = 'button';
  send.className = 'annotation-save';
  send.textContent = 'Reply';
  send.addEventListener('click', async () => {
    const body = input.value.trim();
    if (!body || send.disabled) return;
    send.disabled = true;
    const sent = await postReply(annotation, body);
    send.disabled = false;
    if (sent) {
      if (input.value.trim() === body) {
        input.value = '';
        replyDrafts.delete(annotation.id);
      }
      input.focus({ preventScroll: true });
    }
  });
  input.addEventListener('keydown', submitOnModifierEnter(() => send.click()));

  const row = document.createElement('div');
  row.className = 'annotation-actions annotation-editor-actions';
  row.appendChild(send);
  state.replyForm.append(input, row);
}

// A reply makes sense once the comment itself exists.
function syncReplyForm(annotation, state) {
  state.replyForm.hidden = !annotation.note && state.editing;
}

function submitOnModifierEnter(callback) {
  return (event) => {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      callback();
    }
  };
}

function entryHeader(author, iso) {
  const header = document.createElement('div');
  header.className = 'annotation-entry-head';
  const name = document.createElement('span');
  name.className = 'annotation-author';
  name.textContent = author || 'You';
  header.append(name, timeElement(iso));
  return header;
}

function deliveryLine(annotation) {
  const line = document.createElement('div');
  line.className = 'annotation-delivery';
  const state = deliveryState(annotation);
  if (state.kind === 'unsent') {
    line.textContent = 'Not sent yet';
    return line;
  }
  line.append(state.kind === 'read' ? `Read by ${state.by} ` : 'Sent ', timeElement(state.at));
  return line;
}

function deliveryState(annotation) {
  if (annotation.read_at) return { kind: 'read', at: annotation.read_at, by: annotation.read_by || 'agent' };
  if (annotation.sent_at) return { kind: 'sent', at: annotation.sent_at };
  return { kind: 'unsent' };
}

function clearDeliveryMarks(annotation) {
  for (const field of DELIVERY_FIELDS) delete annotation[field];
}

function bodyElement(text) {
  const body = document.createElement('div');
  body.className = 'annotation-body';
  body.textContent = text;
  return body;
}

function timeElement(iso) {
  const element = document.createElement('time');
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return element;
  element.dateTime = iso;
  element.title = date.toLocaleString();
  element.textContent = compactTime(date);
  return element;
}

function compactTime(date) {
  const now = new Date();
  const time = date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  if (date.toDateString() === now.toDateString()) return time;
  if (date.getFullYear() === now.getFullYear()) {
    return `${date.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${time}`;
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

// Updates the open thread in place; the editor and reply textareas are kept.
function renderOpenThread({ scrollToEnd = false, onlyFor = null } = {}) {
  if (!popover || !popoverState) return;
  const { thread } = popoverState;
  const atBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 4;
  const follow = (scrollToEnd && (!onlyFor || onlyFor === popoverState.id)) || atBottom;
  const annotation = annotations.find((item) => item.id === popoverState.id);
  if (!annotation) {
    closePopover();
    return;
  }
  if (!popoverState.editing) {
    const hadFocus = popoverState.comment.contains(document.activeElement);
    renderComment(annotation, popoverState);
    if (hadFocus) popoverState.comment.querySelector('.annotation-link')?.focus();
  }
  renderReplies(annotation, popoverState);
  placePopover();
  if (follow) thread.scrollTop = thread.scrollHeight;
}

function placePopover() {
  if (!popover || !popoverState) return;
  const margin = 12;
  // Stay inside the document column (and the viewport), not over the sidebars.
  const column = root.closest('.content')?.getBoundingClientRect();
  const minLeft = column ? column.left + margin : margin;
  const maxRight = Math.min(window.innerWidth - margin, column ? column.right - margin : window.innerWidth);
  const width = Math.min(360, Math.max(200, maxRight - minLeft), window.innerWidth - margin * 2);
  popover.style.width = `${width}px`;
  popover.style.maxHeight = `${window.innerHeight - margin * 2}px`;
  const height = popover.offsetHeight;

  const anchorEl = popoverState.anchorEl?.isConnected ? popoverState.anchorEl : null;
  const rect = anchorEl?.getBoundingClientRect() ?? { left: margin, right: margin, top: margin, bottom: margin };
  let left;
  let top;
  if (popoverState.block) {
    left = rect.right + margin;
    top = Math.max(rect.top, margin);
  } else {
    left = rect.left;
    top = rect.bottom + 8;
    if (top + height > window.innerHeight - margin && rect.top - 8 - height >= margin) top = rect.top - 8 - height;
  }
  left = Math.min(Math.max(minLeft, left), maxRight - width);
  left = Math.min(Math.max(margin, left), window.innerWidth - width - margin);
  top = Math.min(Math.max(margin, top), window.innerHeight - height - margin);
  popover.style.left = `${left}px`;
  popover.style.top = `${Math.max(margin, top)}px`;
}

// Keeps the popover attached to its anchor while the document scrolls and
// closes it (saving as usual) once the anchor is entirely out of view.
function onDocumentScroll(event) {
  if (!popover || scrollFrame || popover.contains(event.target)) return;
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = 0;
    if (!popover || !popoverState) return;
    const anchorEl = popoverState.anchorEl;
    // A detached comment opened from the panel has no place in the document.
    if (!anchorEl?.isConnected || anchors.get(popoverState.id)?.detached) return;
    const rect = anchorEl.getBoundingClientRect();
    const view = root.closest('.content')?.getBoundingClientRect();
    const top = view ? view.top : 0;
    const bottom = view ? view.bottom : window.innerHeight;
    if (rect.bottom <= top || rect.top >= bottom) closePopover();
    else placePopover();
  });
}

function closePopover({ restoreFocus = false, save = true } = {}) {
  if (!popover) return;
  const { id, opener, editor } = popoverState;
  const annotation = annotations.find((item) => item.id === id);
  popover.remove();
  popover = null;
  popoverState = null;
  document.removeEventListener('scroll', onDocumentScroll, true);
  cancelAnimationFrame(scrollFrame);
  scrollFrame = 0;

  if (annotation && drafts.has(id)) {
    const text = editor?.value.trim();
    if (text && save) commitComment(annotation, text, 'Comment saved');
    else {
      annotations = annotations.filter((item) => item.id !== id);
      drafts.delete(id);
      removeAnchor(id);
      renderPanel();
    }
  } else if (annotation && save && editor) {
    // Editing an existing comment: keep changed text, treat an emptied editor as no change.
    const text = editor.value.trim();
    if (text && text !== annotation.note) commitComment(annotation, text, 'Comment saved');
  }
  if (restoreFocus) {
    let target = opener?.isConnected ? opener : null;
    if (!target && opener?.classList.contains('comment-item')) {
      target = Array.from(commentsList.querySelectorAll('.comment-item')).find((el) => el.dataset.annotationId === id);
    }
    if (!target) {
      const anchorEl = anchors.get(id)?.focusEl;
      target = anchorEl?.isConnected ? anchorEl : toggle;
    }
    target?.focus();
  }
}

function shorten(text, limit) {
  const flat = text.replace(/\s+/g, ' ').trim();
  return flat.length > limit ? `${flat.slice(0, limit).trimEnd()}\u2026` : flat;
}

// Quote line of the popover: a human label for blocks, a closed curly quote for text.
function quoteLine(annotation) {
  if (annotation.scope === 'block') return `\u25A4 ${blockLabel(annotation)}`;
  const flat = annotation.quote.replace(/\s+/g, ' ').trim();
  const truncated = flat.length > 120;
  return `\u201C${truncated ? `${flat.slice(0, 120).trimEnd()}\u2026` : flat}\u201D`;
}

// Display-only label for a block comment; stored quote and the copied feedback keep the source text.
function blockLabel(annotation) {
  const anchor = anchors.get(annotation.id);
  if (!anchor || anchor.detached || !anchor.nodes[0]?.isConnected) return shorten(annotation.quote, 80);
  const label = singleBlockLabel(anchor.nodes[0]);
  const more = anchor.nodes.length - 1;
  return more > 0 ? `${label} and ${more} more block${more === 1 ? '' : 's'}` : label;
}

function singleBlockLabel(block) {
  return knownBlockKind(block) ?? (shorten(stableText(block), 60) || 'Block');
}

// Kind of a block when it is more than prose (null for paragraphs and headings).
function knownBlockKind(block) {
  if (block.matches('pre.mermaid')) return 'Diagram';
  if (block.classList.contains('code-block-wrapper')) {
    const pre = block.querySelector('pre');
    const language = pre?.dataset.language || /language-([\w+#.-]+)/.exec(pre?.querySelector('code')?.className ?? '')?.[1];
    return language ? `Code block \u00B7 ${language}` : 'Code block';
  }
  const tag = block.tagName.toLowerCase();
  if (tag === 'table') return 'Table';
  if (tag === 'blockquote') return 'Quote';
  if (tag === 'ul' || tag === 'ol') return 'List';
  if (tag === 'hr') return 'Divider';
  if (tag === 'pre') return 'Code block';
  return null;
}

function blockKind(annotation) {
  const anchor = anchors.get(annotation.id);
  if (annotation.scope !== 'block' || !anchor || anchor.detached || !anchor.nodes[0]?.isConnected) return '';
  return knownBlockKind(anchor.nodes[0]) ?? '';
}

// ---------------------------------------------------------------------------
// Floating "Comment" button for selections
// ---------------------------------------------------------------------------

function createFab() {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'annotation-fab';
  button.hidden = true;
  button.textContent = 'Comment';
  button.title = 'Comment on selection (C)';
  button.setAttribute('aria-label', 'Comment on selection');
  button.addEventListener('mousedown', (event) => event.preventDefault());
  button.addEventListener('click', () => {
    const range = fabRange;
    hideFab();
    if (range) commentOnRange(range);
  });
  document.body.appendChild(button);
  return button;
}

function selectionRange() {
  const selection = window.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return null;
  const range = selection.getRangeAt(0);
  if (!root.contains(range.commonAncestorContainer) || !range.toString().trim()) return null;
  return range.cloneRange();
}

function updateFab() {
  if (active || popover) return;
  const range = selectionRange();
  if (!range) {
    hideFab();
    return;
  }
  const rects = range.getClientRects();
  const rect = rects.length ? rects[rects.length - 1] : range.getBoundingClientRect();
  fabRange = range;
  fab.hidden = false;
  const left = Math.min(Math.max(8, rect.right - 8), window.innerWidth - fab.offsetWidth - 8);
  const top = Math.min(rect.bottom + 6, window.innerHeight - fab.offsetHeight - 8);
  fab.style.left = `${left}px`;
  fab.style.top = `${Math.max(8, top)}px`;
}

function hideFab() {
  if (!fab || fab.hidden) return;
  if (document.activeElement === fab) fab.blur();
  fab.hidden = true;
  fabRange = null;
}

// ---------------------------------------------------------------------------
// Comments panel and feedback
// ---------------------------------------------------------------------------

function hasComment(annotation) {
  return Boolean(annotation.note) || annotation.replies.length > 0;
}

function panelEntries() {
  const entries = annotations.filter((item) => !drafts.has(item.id) && hasComment(item));
  const position = (item) => anchors.get(item.id)?.nodes[0];
  return entries.sort((a, b) => {
    const first = position(a);
    const second = position(b);
    if (!first || !second) return Number(!first) - Number(!second);
    return first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1;
  });
}

function renderPanel() {
  if (!commentsList) return;
  if (toggle) toggle.disabled = suspended;
  const entries = panelEntries();
  const focusedId = commentsList.contains(document.activeElement) ? document.activeElement.dataset.annotationId : null;
  commentsList.textContent = '';
  commentsList.hidden = entries.length === 0;
  commentsEmpty.hidden = entries.length > 0;
  const unsentCount = entries.filter((item) => deliveryState(item).kind === 'unsent').length;
  feedbackButton.disabled = suspended || entries.length === 0;
  feedbackButton.textContent = unsentCount > 0 ? `Copy feedback (${unsentCount})` : 'Copy feedback';
  feedbackButton.setAttribute('aria-label', unsentCount > 0 ? `Copy feedback, ${unsentCount} new ${unsentCount === 1 ? 'comment' : 'comments'}` : 'Copy feedback');
  clearButton.disabled = suspended || !annotations.some((item) => !drafts.has(item.id));

  for (const annotation of entries) {
    const detached = anchors.get(annotation.id)?.detached ?? false;
    const item = document.createElement('li');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'comment-item';
    button.dataset.annotationId = annotation.id;
    button.classList.toggle('detached', detached);

    const quote = document.createElement('span');
    quote.className = 'comment-item-quote';
    quote.textContent = annotation.scope === 'block' ? blockLabel(annotation) : shorten(annotation.quote, 80);
    const text = document.createElement('span');
    text.className = 'comment-item-text';
    text.textContent = (annotation.note || annotation.replies[0]?.body || '').replace(/\s+/g, ' ').slice(0, 120);
    const meta = document.createElement('span');
    meta.className = 'comment-item-meta';
    const parts = [annotation.author || 'You'];
    const stamp = new Date(annotation.created_at);
    if (!Number.isNaN(stamp.getTime())) parts.push(compactTime(stamp));
    if (annotation.replies.length > 0) parts.push(annotation.replies.length === 1 ? '1 reply' : `${annotation.replies.length} replies`);
    if (detached) parts.push('detached');
    meta.textContent = parts.join(' \u00B7 ');
    meta.append(deliveryBadge(annotation));

    button.append(quote, text, meta);
    button.addEventListener('click', () => {
      const anchor = anchors.get(annotation.id);
      anchor?.nodes[0]?.scrollIntoView({ block: 'center', behavior: 'auto' });
      openPopover(annotation.id, button);
    });
    item.appendChild(button);
    commentsList.appendChild(item);
  }
  if (focusedId) {
    Array.from(commentsList.querySelectorAll('.comment-item')).find((el) => el.dataset.annotationId === focusedId)?.focus();
  }
}

// Quiet state at the end of the meta line. Unsent is the only one with a dot;
// every state is also text, so it does not rely on colour.
function deliveryBadge(annotation) {
  const state = deliveryState(annotation);
  const badge = document.createElement('span');
  badge.className = `comment-item-state ${state.kind}`;
  if (state.kind === 'unsent') {
    const hidden = document.createElement('span');
    hidden.className = 'visually-hidden';
    hidden.textContent = ' \u00B7 Not sent yet';
    badge.append(hidden);
    return badge;
  }
  const date = new Date(state.at);
  if (!Number.isNaN(date.getTime())) badge.title = date.toLocaleString();
  badge.textContent = ` \u00B7 ${state.kind === 'read' ? `Read by ${state.by}` : 'Sent'}`;
  return badge;
}

function headingBefore(node) {
  let heading = '';
  for (const candidate of root.querySelectorAll('h1, h2, h3, h4, h5, h6')) {
    if (!(candidate.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)) break;
    heading = shorten(stableText(candidate), 120);
  }
  return heading;
}

// Copies the unsent comments (or all of them again when none is unsent), then
// marks the copied ones as sent. The clipboard write must start synchronously
// inside the click, so the text is built from memory before anything is awaited.
function copyFeedback() {
  if (!annotationsReady() || !currentFile) return;
  const all = panelEntries();
  if (all.length === 0) return;
  const unsent = all.filter((item) => deliveryState(item).kind === 'unsent');
  const chosen = unsent.length > 0 ? unsent : all;
  const comments = chosen.map((annotation) => {
    const anchor = anchors.get(annotation.id);
    const kind = blockKind(annotation);
    return {
      id: annotation.id,
      scope: annotation.scope,
      blockCount: annotation.block_count || 1,
      blockKind: kind,
      heading: /^(Diagram|Code block|Table)/.test(kind) ? headingBefore(anchor.nodes[0]) : '',
      quote: annotation.quote,
      detached: anchor?.detached ?? false,
      note: annotation.note,
      replies: annotation.replies,
    };
  });
  const fileName = currentFile.split('/').pop();
  const text = buildFeedback({ fileName, relativePath: currentFile, origin: location.origin, workspaceId, comments });
  if (!navigator.clipboard?.writeText) {
    setStatus('Could not copy feedback');
    return;
  }
  const file = currentFile;
  const ids = chosen.map((item) => item.id);
  const noun = ids.length === 1 ? '1 comment' : `${ids.length} comments`;
  const copied = unsent.length > 0 ? `Feedback copied: ${noun}` : `No new comments: copied all ${ids.length} again`;
  navigator.clipboard.writeText(text).then(
    async () => {
      setStatus(copied, 3000);
      // Copying everything again changes nothing: marking would reset read marks.
      if (unsent.length === 0) return;
      const marked = await markSent(file, ids);
      if (!marked) setStatus('Feedback copied, but could not be marked as sent', 4000);
    },
    () => setStatus('Could not copy feedback'),
  );
}

// Queued behind pending saves so a new comment is stored before it is marked.
function markSent(file, annotationIds) {
  const run = async () => {
    try {
      const response = await fetch(apiUrl('/api/annotations/status', { path: file }), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'sent', annotation_ids: annotationIds }),
      });
      if (!response.ok) throw new Error(response.statusText);
      if (currentFile === file) {
        const loaded = await fetchAnnotations(file);
        if (currentFile === file) mergeServerData(loaded.items);
      }
      return true;
    } catch {
      return false;
    }
  };
  saveQueue = saveQueue.then(run, run);
  return saveQueue;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// Messages with a duration fall back to the highlighter state afterwards.
function setStatus(message, clearAfter = 0) {
  clearTimeout(statusTimer);
  status.textContent = message;
  if (clearAfter) statusTimer = setTimeout(() => { status.textContent = active ? 'Highlighter on' : ''; }, clearAfter);
}

function elementFor(node) {
  return node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
}

function isEditable(element) {
  if (!(element instanceof HTMLElement)) return false;
  return element.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName) || Boolean(element.closest('.annotation-popover'));
}
