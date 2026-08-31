const protocolVersion = '2025-11-25';

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

export function createMcpClient(fetchImpl = window.fetch.bind(window)) {
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
      throw error;
    }
    if (notification) return null;
    return response.json();
  }

  return {
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
