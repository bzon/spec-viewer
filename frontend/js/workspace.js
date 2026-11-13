// The viewer serves each folder under /w/<id>/. Every API call and the WebSocket
// carry that id as `ws`, and the file shown is kept in the URL fragment.

const match = location.pathname.match(/^\/w\/([0-9a-f]+)(?:\/|$)/);

// Null when the page is not under /w/<id>/; calls then omit `ws` (the default workspace).
export const workspaceId = match ? match[1] : null;

// Root of this workspace's static files, always ending in "/".
export const workspaceBase = workspaceId ? `/w/${workspaceId}/` : '/';

function query(params) {
  const pairs = Object.entries(params).map(([key, value]) => `${key}=${encodeURIComponent(value)}`);
  if (workspaceId) pairs.push(`ws=${encodeURIComponent(workspaceId)}`);
  return pairs.length > 0 ? `?${pairs.join('&')}` : '';
}

// apiUrl('/api/file', { path }) -> '/api/file?path=...&ws=<id>'
export function apiUrl(endpoint, params = {}) {
  return endpoint + query(params);
}

export function wsUrl() {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${location.host}/ws${query({})}`;
}

async function errorMessage(response) {
  try {
    return (await response.clone().json()).error;
  } catch {
    return '';
  }
}

// True when the server answered "unknown workspace" (evicted, or restarted with other folders).
export async function isUnknownWorkspace(response) {
  return response.status === 404 && (await errorMessage(response)) === 'unknown workspace';
}

// True when the workspace is known but its folder was deleted or moved.
export async function isFolderGone(response) {
  return response.status === 404 && (await errorMessage(response)) === 'workspace folder no longer exists';
}

export function encodeHashPath(path) {
  return path.split('/').map(encodeURIComponent).join('/');
}

// The document path in the URL fragment, decoded; '' when there is none.
export function readHashPath() {
  const raw = location.hash.slice(1);
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

// Writes the fragment only when it differs, so no extra history entry or hashchange is made.
export function writeHashPath(path) {
  if (readHashPath() === path) return;
  location.hash = encodeHashPath(path);
}

// Resolves a link or image target found in the document at docPath. Returns null for
// anything that is not a plain relative or workspace-root path (schemes, //host, #fragment).
// Leading "/" means the workspace root; ".." cannot climb above it.
export function resolveDocumentTarget(target, docPath) {
  if (!target || target.startsWith('#') || target.startsWith('//') || /^[a-z][a-z0-9+.-]*:/i.test(target)) return null;
  const end = target.search(/[?#]/);
  const pathPart = end === -1 ? target : target.slice(0, end);
  const suffix = end === -1 ? '' : target.slice(end);
  if (!pathPart) return null;
  const segments = pathPart.startsWith('/') ? [] : docPath.split('/').slice(0, -1);
  for (const raw of pathPart.split('/')) {
    if (raw === '' || raw === '.') continue;
    if (raw === '..') { segments.pop(); continue; }
    let segment = raw;
    try { segment = decodeURIComponent(raw); } catch { /* keep as written */ }
    segments.push(segment);
  }
  return { path: segments.join('/'), suffix };
}
