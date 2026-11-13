import { wsUrl } from './workspace.js';

const RECONNECT_MIN_MS = 2000;
const RECONNECT_MAX_MS = 30000;

let socket = null;
let onMessage = null;
let onRefused = null;
let reconnectTimer = null;
let reconnectDelay = RECONNECT_MIN_MS;
let stopped = false;

// onRefused runs (and is awaited) after an attempt that never opened, before the next
// retry is scheduled; it may call disconnect() to end the retries.
export function connect(callback, onConnectionRefused) {
  onMessage = callback;
  onRefused = onConnectionRefused || null;
  stopped = false;
  createConnection();
}

function createConnection() {
  if (stopped) return;
  const current = new WebSocket(wsUrl());
  socket = current;
  let opened = false;

  current.addEventListener('open', () => {
    if (stopped) return;
    opened = true;
    reconnectDelay = RECONNECT_MIN_MS;
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  });

  current.addEventListener('message', (event) => {
    if (stopped) return;
    try {
      const data = JSON.parse(event.data);
      if (onMessage) onMessage(data);
    } catch { /* ignore malformed */ }
  });

  current.addEventListener('close', async () => {
    if (stopped || socket !== current) return;
    if (!opened && onRefused) {
      try { await onRefused(); } catch { /* keep backing off */ }
    }
    if (!stopped) scheduleReconnect();
  });
  current.addEventListener('error', () => current.close());
}

function scheduleReconnect() {
  if (stopped || reconnectTimer) return;
  // Back off (2s, 4s, ... capped at 30s) so a refused endpoint is not hammered.
  reconnectTimer = setTimeout(() => { reconnectTimer = null; createConnection(); }, reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
}

export function disconnect() {
  stopped = true;
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  onMessage = null;
  onRefused = null;
  if (socket) { socket.close(); socket = null; }
}
