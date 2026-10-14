const BLOCK_SELECTOR = 'p, li, h1, h2, h3, h4, td, th';
const CONTEXT_LENGTH = 64;

let root;
let toggle;
let status;
let active = false;
let currentFile = null;
let annotations = [];
let popover = null;

export function initAnnotations(options) {
  root = options.root;
  toggle = options.toggle;
  status = options.status;

  toggle.addEventListener('click', () => setActive(!active));
  root.addEventListener('mouseup', () => {
    if (active) setTimeout(highlightSelection, 0);
  });
  root.addEventListener('click', (event) => {
    const mark = event.target.closest('.annotation-highlight');
    if (mark) openPopover(mark.dataset.annotationId, mark);
  });
  root.addEventListener('keydown', (event) => {
    const mark = event.target.closest('.annotation-highlight');
    if (mark && (event.key === 'Enter' || event.key === ' ')) {
      event.preventDefault();
      openPopover(mark.dataset.annotationId, mark);
    }
  });
  document.addEventListener('mousedown', (event) => {
    if (popover && !popover.contains(event.target) && !event.target.closest('.annotation-highlight')) {
      closePopover();
    }
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      if (popover) closePopover();
      else if (active) setActive(false);
      return;
    }
    if (event.key.toLowerCase() === 'h' && !event.metaKey && !event.ctrlKey && !event.altKey && !isEditable(event.target)) {
      event.preventDefault();
      setActive(!active);
    }
  });
}

export async function loadAnnotations(filePath) {
  currentFile = filePath;
  annotations = [];
  closePopover();

  try {
    const response = await fetch('/api/annotations?path=' + encodeURIComponent(filePath));
    if (!response.ok) throw new Error(response.statusText);
    const loaded = await response.json();
    if (currentFile !== filePath) return;
    annotations = loaded;
    applyAnnotations();
    setStatus('');
  } catch {
    if (currentFile === filePath) setStatus('Could not load highlights');
  }
}

function setActive(next) {
  active = next;
  toggle.setAttribute('aria-pressed', String(active));
  toggle.classList.toggle('active', active);
  root.classList.toggle('highlight-mode', active);
  setStatus(active ? 'Highlighter on' : '');
  if (!active) window.getSelection()?.removeAllRanges();
}

function highlightSelection() {
  const selection = window.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return;

  const range = selection.getRangeAt(0);
  const startElement = elementFor(range.startContainer);
  const endElement = elementFor(range.endContainer);
  const block = startElement?.closest(BLOCK_SELECTOR);
  if (!block || block !== endElement?.closest(BLOCK_SELECTOR) || !root.contains(block)) {
    setStatus('Select text within one paragraph or list item');
    selection.removeAllRanges();
    return;
  }
  if (range.cloneContents().querySelector?.('.annotation-highlight') || startElement.closest('.annotation-highlight') || endElement.closest('.annotation-highlight')) {
    setStatus('That selection already contains a highlight');
    selection.removeAllRanges();
    return;
  }

  const quote = range.toString();
  if (!quote.trim()) {
    selection.removeAllRanges();
    return;
  }

  const before = document.createRange();
  before.selectNodeContents(block);
  before.setEnd(range.startContainer, range.startOffset);
  const start = before.toString().length;
  const blockText = block.textContent;
  const now = new Date().toISOString();
  const annotation = {
    id: crypto.randomUUID(),
    quote,
    prefix: blockText.slice(Math.max(0, start - CONTEXT_LENGTH), start),
    suffix: blockText.slice(start + quote.length, start + quote.length + CONTEXT_LENGTH),
    note: '',
    created_at: now,
    updated_at: now,
  };

  annotations.push(annotation);
  applyAnnotation(annotation);
  selection.removeAllRanges();
  saveAnnotations();
}

function applyAnnotations() {
  for (const annotation of annotations) applyAnnotation(annotation);
}

function applyAnnotation(annotation) {
  let best = null;
  for (const block of root.querySelectorAll(BLOCK_SELECTOR)) {
    const text = block.textContent;
    let index = text.indexOf(annotation.quote);
    while (index !== -1) {
      if (!overlapsHighlight(block, index, index + annotation.quote.length)) {
        const score = contextScore(text, index, annotation);
        if (!best || score > best.score) best = { block, index, score };
      }
      index = text.indexOf(annotation.quote, index + 1);
    }
  }
  if (!best) return;

  const marks = wrapText(best.block, best.index, best.index + annotation.quote.length, annotation.id);
  for (const mark of marks) updateMark(mark, annotation);
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

function overlapsHighlight(block, start, end) {
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
  let offset = 0;
  let node;
  while ((node = walker.nextNode())) {
    const nodeEnd = offset + node.data.length;
    if (nodeEnd > start && offset < end && node.parentElement.closest('.annotation-highlight')) return true;
    offset = nodeEnd;
  }
  return false;
}

function wrapText(block, start, end, id) {
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
  const matches = [];
  let offset = 0;
  let node;
  while ((node = walker.nextNode())) {
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

async function saveAnnotations() {
  if (!currentFile) return;
  try {
    const response = await fetch('/api/annotations?path=' + encodeURIComponent(currentFile), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ annotations }),
    });
    if (!response.ok) throw new Error(response.statusText);
    setStatus(active ? 'Highlighter on' : 'Saved');
    if (!active) setTimeout(() => setStatus(''), 1200);
  } catch {
    setStatus('Could not save highlights');
  }
}

function openPopover(id, anchor) {
  const annotation = annotations.find((item) => item.id === id);
  if (!annotation) return;
  closePopover();

  popover = document.createElement('div');
  popover.className = 'annotation-popover';
  popover.setAttribute('role', 'dialog');
  popover.setAttribute('aria-label', 'Highlight note');

  const textarea = document.createElement('textarea');
  textarea.className = 'annotation-note';
  textarea.rows = 3;
  textarea.maxLength = 10000;
  textarea.placeholder = 'Add a note';
  textarea.value = annotation.note || '';
  textarea.setAttribute('aria-label', 'Note');

  const actions = document.createElement('div');
  actions.className = 'annotation-actions';

  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'annotation-remove';
  remove.textContent = 'Remove';
  remove.addEventListener('click', () => {
    if (window.confirm('Remove this highlight?')) removeAnnotation(id);
  });

  const save = document.createElement('button');
  save.type = 'button';
  save.className = 'annotation-save';
  save.textContent = 'Save note';
  save.addEventListener('click', () => {
    annotation.note = textarea.value.trim();
    annotation.updated_at = new Date().toISOString();
    document.querySelectorAll(`[data-annotation-id="${CSS.escape(id)}"]`).forEach((mark) => updateMark(mark, annotation));
    closePopover();
    saveAnnotations();
  });

  actions.append(remove, save);
  popover.append(textarea, actions);
  document.body.appendChild(popover);
  positionPopover(popover, anchor);
  textarea.focus();
}

function removeAnnotation(id) {
  annotations = annotations.filter((annotation) => annotation.id !== id);
  document.querySelectorAll(`[data-annotation-id="${CSS.escape(id)}"]`).forEach((mark) => {
    const parent = mark.parentNode;
    mark.replaceWith(...mark.childNodes);
    parent.normalize();
  });
  closePopover();
  saveAnnotations();
}

function updateMark(mark, annotation) {
  const hasNote = Boolean(annotation.note);
  mark.classList.toggle('has-note', hasNote);
  mark.setAttribute('tabindex', '0');
  mark.setAttribute('role', 'button');
  mark.setAttribute('aria-label', hasNote ? 'Highlighted text with note. Activate to edit.' : 'Highlighted text. Activate to add a note.');
}

function positionPopover(element, anchor) {
  const rect = anchor.getBoundingClientRect();
  const width = Math.min(320, window.innerWidth - 24);
  const left = Math.min(Math.max(12, rect.left), window.innerWidth - width - 12);
  const top = Math.min(rect.bottom + 8, window.innerHeight - 180);
  element.style.width = `${width}px`;
  element.style.left = `${left}px`;
  element.style.top = `${Math.max(12, top)}px`;
}

function closePopover() {
  popover?.remove();
  popover = null;
}

function setStatus(message) {
  status.textContent = message;
}

function elementFor(node) {
  return node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
}

function isEditable(element) {
  return element instanceof HTMLElement && (element.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName));
}
