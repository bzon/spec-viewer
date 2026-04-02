let socket = null;
let onMessage = null;
let reconnectTimer = null;

export function connect(callback) {
  onMessage = callback;
  createConnection();
}

function createConnection() {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = protocol + '//' + location.host + '/ws';

  socket = new WebSocket(url);

  socket.addEventListener('open', () => {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  });

  socket.addEventListener('message', (event) => {
    try {
      const data = JSON.parse(event.data);
      if (onMessage) onMessage(data);
    } catch { /* ignore malformed */ }
  });

  socket.addEventListener('close', () => scheduleReconnect());
  socket.addEventListener('error', () => socket.close());
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => { reconnectTimer = null; createConnection(); }, 2000);
}

export function disconnect() {
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  if (socket) { onMessage = null; socket.close(); socket = null; }
}
