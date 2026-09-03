const protocolVersion = '2025-11-25';

function parseSSEEvent(event) {
  const data = event.split(/\r?\n/)
    .filter(line => line.startsWith('data:'))
    .map(line => line.slice(5).replace(/^ /, ''))
    .join('\n')
    .trim();
  if (!data || data === '[DONE]') return null;
  return JSON.parse(data);
}

function parseSSEText(text) {
  for (const event of String(text || '').split(/\r\n\r\n|\n\n|\r\r/)) {
    const parsed = parseSSEEvent(event);
    if (parsed !== null) return parsed;
  }
  throw new Error('MCP SSE 응답이 비어 있습니다.');
}

async function readMcpResponse(response) {
  const contentType = (response.headers.get('content-type') || '').toLowerCase();
  if (!contentType.includes('text/event-stream')) return response.json();

  const reader = response.body && typeof response.body.getReader === 'function' ? response.body.getReader() : null;
  if (!reader) return parseSSEText(await response.text());

  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (true) {
      const chunk = await reader.read();
      buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done });
      let boundary;
      while ((boundary = /\r\n\r\n|\n\n|\r\r/.exec(buffer)) !== null) {
        const event = buffer.slice(0, boundary.index);
        buffer = buffer.slice(boundary.index + boundary[0].length);
        const parsed = parseSSEEvent(event);
        if (parsed !== null) {
          try { await reader.cancel(); } catch (_) { /* the response may already be closed */ }
          return parsed;
        }
      }
      if (chunk.done) break;
    }
    return parseSSEText(buffer);
  } finally {
    if (typeof reader.releaseLock === 'function') reader.releaseLock();
  }
}

export function toolValue(data) {
  const content = data?.result?.content;
  if (Array.isArray(content) && content.length && typeof content[0]?.text === 'string') {
    try { return JSON.parse(content[0].text); } catch (_) { return data; }
  }
  return data;
}

export function pageItems(value) {
  if (Array.isArray(value)) return value;
  if (value && Array.isArray(value.items)) return value.items;
  return [];
}

export function createMcpClient(fetchImpl = window.fetch.bind(window), options = {}) {
  let sessionId = '';
  let requestId = 0;

  async function request(method, params, notification = false) {
    const headers = {
      Accept: 'application/json, text/event-stream',
      'Content-Type': 'application/json'
    };
    const body = { jsonrpc: '2.0', method, params };
    if (!notification) body.id = ++requestId;
    if (sessionId) headers['Mcp-Session-Id'] = sessionId;
    const response = await fetchImpl('/mcp', { method: 'POST', headers, credentials: 'same-origin', body: JSON.stringify(body) });
    const responseSession = response.headers.get('Mcp-Session-Id');
    if (responseSession) sessionId = responseSession;
    if (!response.ok) {
      const error = new Error(response.status === 401 ? '로그인이 필요합니다.' : `MCP 연결 오류 (${response.status})`);
      error.status = response.status;
      if (error.status === 401) {
        sessionId = '';
        if (typeof options.onAuthenticationExpired === 'function') options.onAuthenticationExpired();
      }
      throw error;
    }
    if (notification) return null;
    return readMcpResponse(response);
  }

  return {
    setAuthenticationExpiredHandler(handler) { options.onAuthenticationExpired = handler; },
    async initialize() {
      if (sessionId) return;
      await request('initialize', {
        protocolVersion,
        capabilities: {},
        clientInfo: { name: 'stash-vue-monitor', version: '0.1.0' }
      });
      await request('notifications/initialized', {}, true);
    },
    async call(tool, args) {
      await this.initialize();
      try {
        return toolValue(await request('tools/call', { name: tool, arguments: args }));
      } catch (error) {
        if (error.status === 404) {
          sessionId = '';
          await this.initialize();
          return toolValue(await request('tools/call', { name: tool, arguments: args }));
        }
        throw error;
      }
    }
  };
}
