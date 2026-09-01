(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashApiClient = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createApiClient() {
        return {
            adminHeaders() {
                const headers = { 'Accept': 'application/json' };
                if (this.adminToken) headers['X-Stash-Admin-Token'] = this.adminToken;
                return headers;
            },

            async adminRequest(path, options = {}) {
                const headers = new Headers(options.headers || {});
                for (const [key, value] of Object.entries(this.adminHeaders())) headers.set(key, value);
                const res = await fetch(path, { ...options, headers });
                let body = null;
                try { body = await res.json(); } catch (_) { /* keep the status text */ }
                if (!res.ok) {
                    const message = body && body.error ? body.error : `HTTP ${res.status}`;
                    const error = new Error(message);
                    error.status = res.status;
                    throw error;
                }
                return body || {};
            },

            async invokeTool(toolName, args) {
                const run = async () => {
                    await this.initializeSession();
                    return this.sendMCPRequest('tools/call', { name: toolName, arguments: args });
                };
                try {
                    return await run();
                } catch (e) {
                    if (e.status === 401) {
                        this.sessionId = '';
                        if (typeof this.markAuthenticationExpired === 'function') {
                            this.markAuthenticationExpired();
                        }
                    }
                    // A server restart invalidates the previous session ID.
                    if (e.status === 404 && this.sessionId) {
                        this.sessionId = '';
                        return await run();
                    }
                    throw e;
                }
            },

            toolValue(data) {
                if (data && data.result && data.result.content && data.result.content.length > 0) {
                    try {
                        return JSON.parse(data.result.content[0].text);
                    } catch (_) {
                        return data;
                    }
                }
                return data;
            },

            pageSlice(value, limit, offset) {
                if (value && Array.isArray(value.items) && value.has_more !== undefined) {
                    return {
                        isPage: true,
                        items: value.items,
                        hasMore: Boolean(value.has_more),
                        nextOffset: Number(value.next_offset) || offset + value.items.length
                    };
                }
                if (Array.isArray(value)) {
                    const items = value.slice(0, limit);
                    return {
                        isPage: true,
                        items,
                        hasMore: value.length > limit,
                        nextOffset: offset + items.length
                    };
                }
                return { isPage: false, items: [], hasMore: false, nextOffset: offset };
            },

            async initializeSession() {
                if (this.sessionId) {
                    return;
                }
                await this.sendMCPRequest('initialize', {
                    protocolVersion: '2025-11-25',
                    capabilities: {},
                    clientInfo: { name: 'stash-console', version: '0.2.8' }
                });
                await this.sendMCPRequest('notifications/initialized', {}, true, true);
            },

            async sendMCPRequest(method, params, returnResponse = true, notification = false) {
                const headers = {
                    'Accept': 'application/json, text/event-stream',
                    'Content-Type': 'application/json'
                };
                if (this.token) {
                    headers['Authorization'] = 'Bearer ' + this.token;
                }
                if (this.sessionId) {
                    headers['Mcp-Session-Id'] = this.sessionId;
                }

                const body = { jsonrpc: '2.0', method, params };
                if (!notification) {
                    body.id = ++this.requestId;
                }
                const res = await fetch('/mcp', {
                    method: 'POST',
                    headers,
                    body: JSON.stringify(body),
                    credentials: 'same-origin'
                });
                if (!res.ok) {
                    const error = new Error(`HTTP 오류 ${res.status}: ${res.statusText}`);
                    error.status = res.status;
                    throw error;
                }
                const responseSessionId = res.headers.get('Mcp-Session-Id');
                if (responseSessionId) {
                    this.sessionId = responseSessionId;
                }
                if (notification || !returnResponse) {
                    return null;
                }
                return res.json();
            }
        };
    }

    return { createApiClient };
}));
