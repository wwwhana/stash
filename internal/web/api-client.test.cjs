const test = require('node:test');
const assert = require('node:assert/strict');

const { createApiClient } = require('./ui/api-client.js');

test('the common API module owns HTTP and MCP transport helpers', () => {
    const api = createApiClient();

    for (const method of ['adminRequest', 'invokeTool', 'toolValue', 'pageSlice', 'initializeSession', 'sendMCPRequest']) {
        assert.equal(typeof api[method], 'function', method);
    }
});

test('a rejected MCP request clears the stale session and expires the console auth state', async () => {
    const api = createApiClient();
    let expired = 0;
    Object.assign(api, {
        sessionId: 'stale-session',
        initializeSession: async () => {},
        sendMCPRequest: async () => {
            const error = new Error('HTTP 401');
            error.status = 401;
            throw error;
        },
        markAuthenticationExpired() { expired += 1; }
    });

    await assert.rejects(api.invokeTool('list_namespaces', {}), error => error.status === 401);
    assert.equal(api.sessionId, '');
    assert.equal(expired, 1);
});

test('the MCP client reads a streamable SSE response without waiting for EOF', async () => {
    const api = createApiClient();
    api.requestId = 0;
    const encoder = new TextEncoder();
    let reads = 0;
    let canceled = false;
    let released = false;
    const previousFetch = global.fetch;
    global.fetch = async () => ({
        ok: true,
        status: 200,
        statusText: 'OK',
        headers: { get(name) { return name.toLowerCase() === 'content-type' ? 'text/event-stream' : ''; } },
        body: {
            getReader() {
                return {
                    async read() {
                        reads += 1;
                        if (reads === 1) return { value: encoder.encode('event: message\ndata: {"jsonrpc":"2.0","id":1,'), done: false };
                        if (reads === 2) return { value: encoder.encode('"result":{"ok":true}}\n\n'), done: false };
                        throw new Error('the client waited for EOF');
                    },
                    async cancel() { canceled = true; },
                    releaseLock() { released = true; }
                };
            }
        }
    });
    try {
        const response = await api.sendMCPRequest('ping', {});
        assert.deepEqual(response, { jsonrpc: '2.0', id: 1, result: { ok: true } });
        assert.equal(canceled, true);
        assert.equal(released, true);
    } finally {
        global.fetch = previousFetch;
    }
});
