import { initRenderer, render, setTheme } from './render.js';
import { buildTOC, destroyTOC } from './toc.js';
import { connect } from './ws.js';
import { buildIndex, search } from './search.js';

const state = { currentFile: null, isDirectory: false, files: [], themes: [], searchIndex: null, searchOpen: false, selectedResult: 0 };

const els = {
  fileList: document.getElementById('file-list'),
  files: document.getElementById('files'),
  content: document.getElementById('content'),
  rendered: document.getElementById('rendered'),
  toc: document.getElementById('toc'),
  themeSelect: document.getElementById('theme-select'),
  themeCss: document.getElementById('theme-css'),
};

async function init() {
  els.rendered.innerHTML = '<div class="loading">Loading renderer</div>';
  await initRenderer();
  els.rendered.innerHTML = '';
  await loadThemes();

  const hashPath = location.hash.slice(1);
  const filesResp = await fetch('/api/files');
  state.files = await filesResp.json();
  state.isDirectory = state.files.length > 1;

  if (state.isDirectory) { els.fileList.classList.add('visible'); renderFileList(); }
  if (hashPath) await loadFile(hashPath);
  else if (state.files.length > 0) await loadFile(state.files[0].path);

  await buildSearchIndex();

  els.themeSelect.addEventListener('change', (e) => applyTheme(e.target.value));

  connect((msg) => {
    if (msg.type === 'reload' && msg.path) {
      if (msg.path === state.currentFile) reloadCurrentFile();
      refreshFileList();
      buildSearchIndex();
    }
    if (msg.type === 'navigate' && msg.path) loadFile(msg.path);
  });

  window.addEventListener('hashchange', () => {
    const path = location.hash.slice(1);
    if (path && path !== state.currentFile) loadFile(path);
  });

  initSearch();
}

async function buildSearchIndex() {
  const entries = [];
  for (const file of state.files) {
    const resp = await fetch('/api/file?path=' + encodeURIComponent(file.path));
    if (resp.ok) {
      const content = await resp.text();
      entries.push({ path: file.path, name: file.name, content });
    }
  }
  state.searchIndex = buildIndex(entries);
}

async function loadFile(filePath) {
  state.currentFile = filePath;
  location.hash = filePath;

  const resp = await fetch('/api/file?path=' + encodeURIComponent(filePath));
  if (!resp.ok) { els.rendered.textContent = 'Failed to load: ' + resp.statusText; return; }

  const markdown = await resp.text();
  els.rendered.innerHTML = render(markdown);
  addCopyButtons(els.rendered);
  destroyTOC();
  buildTOC(els.toc, els.rendered);

  document.querySelectorAll('.file-item').forEach((el) => {
    if (el.dataset.path === filePath) el.classList.add('active');
    else el.classList.remove('active');
  });

  const h1 = els.rendered.querySelector('h1');
  document.title = h1 ? h1.textContent + ' \u2014 spec-viewer' : 'spec-viewer';
}

async function reloadCurrentFile() {
  if (!state.currentFile) return;
  const scrollTop = els.content.scrollTop;
  const resp = await fetch('/api/file?path=' + encodeURIComponent(state.currentFile));
  if (!resp.ok) return;
  els.rendered.innerHTML = render(await resp.text());
  addCopyButtons(els.rendered);
  destroyTOC();
  buildTOC(els.toc, els.rendered);
  els.content.scrollTop = scrollTop;
}

function renderFileList() {
  els.files.textContent = '';

  // Group files by directory
  const groups = {};
  state.files.forEach((file) => {
    const parts = file.name.split('/');
    const dir = parts.length > 1 ? parts.slice(0, -1).join('/') : '';
    if (!groups[dir]) groups[dir] = [];
    groups[dir].push(file);
  });

  // Sort directories: root first, then alphabetical
  const dirs = Object.keys(groups).sort((a, b) => {
    if (a === '') return -1;
    if (b === '') return 1;
    return a.localeCompare(b);
  });

  dirs.forEach((dir) => {
    if (dir !== '') {
      const dirLabel = document.createElement('div');
      dirLabel.className = 'file-group-label';
      dirLabel.textContent = dir;
      els.files.appendChild(dirLabel);
    }

    groups[dir].forEach((file) => {
      const item = document.createElement('button');
      item.type = 'button';
      item.className = 'file-item';
      if (dir !== '') item.classList.add('file-item-nested');
      // Show just the filename, not the full path
      const fileName = file.name.split('/').pop();
      item.textContent = fileName;
      item.dataset.path = file.path;
      item.addEventListener('click', () => loadFile(file.path));
      els.files.appendChild(item);
    });
  });
}

async function refreshFileList() {
  const resp = await fetch('/api/files');
  state.files = await resp.json();
  if (state.isDirectory) {
    renderFileList();
    document.querySelectorAll('.file-item').forEach((el) => {
      if (el.dataset.path === state.currentFile) el.classList.add('active');
      else el.classList.remove('active');
    });
  }
  if (state.currentFile && !state.files.find((f) => f.path === state.currentFile)) {
    if (state.files.length > 0) await loadFile(state.files[0].path);
  }
}

async function loadThemes() {
  const resp = await fetch('/api/themes');
  state.themes = await resp.json();
  els.themeSelect.textContent = '';
  state.themes.forEach((theme) => {
    const opt = document.createElement('option');
    opt.value = theme;
    opt.textContent = theme;
    els.themeSelect.appendChild(opt);
  });
  const match = els.themeCss.getAttribute('href').match(/themes\/(.+)\.css/);
  if (match) els.themeSelect.value = match[1];
}

function applyTheme(themeName) {
  els.themeCss.href = '/css/themes/' + themeName + '.css';
  setTheme(themeName);
  if (state.currentFile) reloadCurrentFile();
}

function initSearch() {
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      if (state.searchOpen) {
        closeSearch();
      } else {
        openSearch();
      }
    }
    if (e.key === 'Escape' && state.searchOpen) {
      closeSearch();
    }
  });
}

function openSearch() {
  state.searchOpen = true;
  state.selectedResult = 0;

  const backdrop = document.createElement('div');
  backdrop.className = 'search-backdrop';
  backdrop.addEventListener('click', (e) => {
    if (e.target === backdrop) closeSearch();
  });

  const modal = document.createElement('div');
  modal.className = 'search-modal';

  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'search-input';
  input.placeholder = 'Search headings, files, and content...';
  input.setAttribute('aria-label', 'Search');

  const results = document.createElement('div');
  results.className = 'search-results';

  const hint = document.createElement('div');
  hint.className = 'search-hint';
  hint.innerHTML = '<kbd>\u2191\u2193</kbd> navigate <kbd>\u21B5</kbd> open <kbd>esc</kbd> close';

  let debounceTimer = null;
  input.addEventListener('input', () => {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => renderSearchResults(results, input.value), 150);
  });

  input.addEventListener('keydown', (e) => {
    const buttons = results.querySelectorAll('.search-result');
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      state.selectedResult = Math.min(state.selectedResult + 1, buttons.length - 1);
      updateSelectedResult(results);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      state.selectedResult = Math.max(state.selectedResult - 1, 0);
      updateSelectedResult(results);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const selected = results.querySelector('.search-result.selected');
      if (selected) selected.click();
    }
  });

  modal.appendChild(input);
  modal.appendChild(results);
  modal.appendChild(hint);
  backdrop.appendChild(modal);
  document.body.appendChild(backdrop);
  input.focus();
}

function closeSearch() {
  const backdrop = document.querySelector('.search-backdrop');
  if (backdrop) backdrop.remove();
  state.searchOpen = false;
}

function renderSearchResults(container, query) {
  container.textContent = '';
  if (!query || !state.searchIndex) return;

  const results = search(query, state.searchIndex);
  state.selectedResult = 0;

  if (results.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'search-empty';
    empty.textContent = 'No matches';
    container.appendChild(empty);
    return;
  }

  results.forEach((result, i) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'search-result' + (i === 0 ? ' selected' : '');

    const icon = document.createElement('span');
    icon.className = 'search-result-icon';
    if (result.type === 'heading') icon.textContent = '#';
    else if (result.type === 'file') icon.textContent = '\uD83D\uDCC4';
    else icon.textContent = 'Aa';

    const text = document.createElement('span');
    text.className = 'search-result-text';
    text.innerHTML = highlightMatch(result.display, query);

    btn.appendChild(icon);
    btn.appendChild(text);

    btn.addEventListener('click', () => {
      closeSearch();
      loadFile(result.filePath).then(() => {
        if (result.headingId) {
          const target = document.getElementById(result.headingId);
          if (target) target.scrollIntoView({ behavior: 'smooth' });
        }
      });
    });

    container.appendChild(btn);
  });
}

function updateSelectedResult(container) {
  const buttons = container.querySelectorAll('.search-result');
  buttons.forEach((btn, i) => {
    if (i === state.selectedResult) {
      btn.classList.add('selected');
      btn.scrollIntoView({ block: 'nearest' });
    } else {
      btn.classList.remove('selected');
    }
  });
}

function addCopyButtons(container) {
  container.querySelectorAll('pre').forEach((pre) => {
    if (pre.querySelector('.copy-btn')) return;
    const wrapper = document.createElement('div');
    wrapper.className = 'code-block-wrapper';
    pre.parentNode.insertBefore(wrapper, pre);
    wrapper.appendChild(pre);

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy-btn';
    btn.setAttribute('aria-label', 'Copy code');
    btn.textContent = 'Copy';
    btn.addEventListener('click', () => {
      const code = pre.querySelector('code') || pre;
      navigator.clipboard.writeText(code.textContent).then(() => {
        btn.textContent = 'Copied!';
        setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
      });
    });
    wrapper.appendChild(btn);
  });
}

function escapeHtml(str) {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function highlightMatch(text, query) {
  const escaped = escapeHtml(text);
  const escapedQuery = escapeHtml(query);
  const idx = escaped.toLowerCase().indexOf(escapedQuery.toLowerCase());
  if (idx === -1) return escaped;
  return escaped.slice(0, idx) + '<mark>' + escaped.slice(idx, idx + escapedQuery.length) + '</mark>' + escaped.slice(idx + escapedQuery.length);
}

init().catch(console.error);
