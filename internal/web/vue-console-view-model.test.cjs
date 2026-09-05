const test = require('node:test');
const assert = require('node:assert/strict');
const { createViewModel } = require('./ui/vue-console-view-model.js');
const { createApiClient } = require('./ui/api-client.js');
const routeAPI = require('./ui/route-state.js');
const goalMap = require('./ui/goal-map-layout.js');
const workGraph = require('./ui/work-graph-layout.js');
const search = require('./ui/search-utils.js');

function setup(path = '/ui/goal-map', invoke = async () => ({})) {
    const window = {
        location: new URL(path, 'http://stash.local'),
        document: { documentElement: { dataset: {} }, title: '' },
        fetch: async () => ({ ok: true, json: async () => ({ auth_mode: 'none', authenticated: false }) })
    };
    window.history = Object.fromEntries(['pushState', 'replaceState'].map(method => [method, (_, __, url) => { window.location = new URL(url, window.location); }]));
    const api = Object.assign(createApiClient(), { invokeTool: invoke });
    const options = createViewModel({ api, routeAPI, goalMap, workGraph, search, window });
    const state = options.data();
    for (const [key, method] of Object.entries(options.methods)) state[key] = method.bind(state);
    for (const [key, get] of Object.entries(options.computed)) Object.defineProperty(state, key, { get: () => get.call(state) });
    return { state, window, api };
}

const map = {
    goal_tree: { root_goal_id: 1, goals: [{ id: 1, content: '공통 목표', status: 'active', subtree_work_total: 2, subtree_work_done: 1, progress: 0.5 }] },
    work_items: [{ id: 2, goal_id: 1, title: '작업', status: 'doing' }],
    memories: [{ key: 'memory:fact:3', memory_type: 'fact', memory_id: 3, content: '결정의 근거' }],
    edges: [{ from: 'work:2', to: 'goal:1', relation: 'contributes_to' }, { from: 'memory:fact:3', to: 'work:2', relation: 'context' }]
};

test('login is checked before any namespace or work request', async () => {
    let requests = 0;
    const { state, window } = setup('/', async () => { requests++; });
    window.fetch = async () => ({ ok: true, json: async () => ({ auth_mode: 'oidc', authenticated: false }) });
    await state.bootstrap();
    assert.equal(requests, 0);
    assert.equal(state.needsLogin, true);
    assert.equal(state.authChecked, true);
    assert.equal(state.authLoading, false);
    window.fetch = async () => ({ ok: false });
    const failed = setup('/');
    failed.window.fetch = window.fetch;
    await failed.state.bootstrap();
    assert.equal(failed.state.authChecked, false);
    assert.match(failed.state.t(failed.state.error), /로그인 상태/);
});

test('old requests cannot overwrite a newly selected workspace', async () => {
    let resolveOld;
    const { state } = setup('/ui/goal-map?namespace=/old', (_, args) => args.namespace === '/old' ? new Promise(resolve => { resolveOld = resolve; }) : Promise.resolve(map));
    const old = state.loadRoute();
    state.rootSlug = '/new';
    await state.changeRoot();
    resolveOld({ goal_tree: { goals: [{ id: 99 }] } });
    await old;
    assert.equal(state.map.goal_tree.goals[0].id, 1);
    assert.equal(state.rootSlug, '/new');
});

test('navigation keeps the workspace, resets incompatible filters, and restores the default scope on Back', async () => {
    const { state, window } = setup('/ui/monitor?namespace=/personal&status=doing&q=hello');
    await state.navigate('list_hypotheses');
    assert.equal(state.rootSlug, '/personal');
    assert.equal(state.filters.status, '');
    assert.equal(state.filters.query, '');
    assert.deepEqual(state.statusOptions, ['proposed', 'testing', 'confirmed', 'rejected']);
    window.location = new URL('http://stash.local/ui/goal-map');
    state.handlePopState();
    assert.equal(state.rootSlug, '/');
    assert.match(routeAPI.buildRoute('monitor', { namespace: '/personal' }), /namespace=/);
});

test('work, goals and typed memories have bidirectional detail links without duplicate edges', async () => {
    const { state } = setup('/', async () => map);
    await state.loadRoute();
    state.selectObject('work', state.allWork[0]);
    assert.deepEqual(state.selectedConnections.map(item => item.kind).sort(), ['goal', 'memory']);
    state.selectObject('memory', { id: 3, memory_type: 'fact', content: '원본 기억' });
    assert.equal(state.selected.key, 'memory:fact:3');
    assert.equal(state.selectedTitle, '사실 #3');
    assert.equal(state.selectedFields.some(field => field.label === '상태'), false);
    assert.deepEqual(state.selectedConnections.map(item => item.kind), ['work']);
    assert.deepEqual(state.selectedChildren, []);
    assert.equal(state.mapItemKey('memory', { id: 3, memory_type: 'hypothesis' }), 'memory:hypothesis:3');
    state.selectObject('goal', { id: 1, content: '공통 목표', status: 'active' });
    assert.match(state.selectedFields.find(field => field.label === '진행 상황').value, /2개 중 1개/);
    assert.equal(state.goalProgressLabel({ status: 'active' }), '연결된 작업 없음');
});

test('all namespace pages and shortened MCP pages remain reachable', async () => {
    const calls = [];
    const { state } = setup('/ui/facts', async (tool, args) => {
        calls.push({ tool, args });
        if (tool === 'get_goal_map') return map;
        if (!args.offset) return { items: [{ id: 4, slug: '/a', content: '첫 기록' }], has_more: true, next_offset: 1 };
        return [{ id: 5, slug: '/b', content: '다음 기록' }];
    });
    await state.fetchNamespaces();
    assert.deepEqual(state.namespaces.map(item => item.slug), ['/a', '/b']);
    await state.loadRoute();
    await state.nextPage();
    assert.deepEqual(state.listItems.map(item => item.id), [4, 5]);
    assert.equal(state.listItems[0].memory_type, 'fact');
    assert.equal(state.route.offset, 1);
    state.filters.query = '다음';
    await state.searchList();
    assert.equal(state.route.offset, 0);
    assert.equal(calls.at(-1).args.q, '다음');
    assert.equal('status' in calls.at(-1).args, false);
});

test('board loads complete issue fields and retains canceled and expired work', async () => {
    const requests = [];
    const { state } = setup('/ui/issues?type=bug&label=ui', async (tool, args) => {
        requests.push({ tool, args });
        return tool === 'get_goal_map' ? map : [{ id: 10, status: 'canceled', issue_type: 'bug' }, { id: 11, status: 'doing', attempt_status: 'active', lease_expires_at: '2000-01-01' }];
    });
    await state.loadRoute();
    assert.equal(requests.at(-1).tool, 'list_work_items');
    assert.equal(requests.at(-1).args.label, 'ui');
    assert.equal(requests.at(-1).args.issue_type, 'bug');
    assert.equal(state.boardColumns.find(column => column.status === 'canceled').items.length, 1);
    assert.equal(state.boardColumns.find(column => column.status === 'expired').items.length, 1);
});

test('Git list is rendered, namespace search works, and missing progress is not shown as zero', async () => {
    const { state } = setup('/ui/git?namespace=/personal', async () => [{ id: 1, repository: '저장소', worktree_path: '/workspace/stash', branch: 'main' }]);
    await state.loadRoute();
    assert.equal(state.isListRoute, true);
    assert.equal(state.listItems.length, 1);
    assert.deepEqual(state.statusOptions, []);
    state.selectListItem(state.listItems[0]);
    assert.equal(state.selectedTitle, '저장소');
    assert.equal(state.selectedFields.find(field => field.label === '브랜치').value, 'main');
    state.route.route = 'list_namespaces';
    state.filters.query = '없는 공간';
    assert.equal(state.visibleListItems.length, 0);
    assert.equal(state.mapLoaded, false);
});

test('restoring a memory detail prefers its full list content over the map excerpt', async () => {
    const { state } = setup('/ui/facts?focus=memory:fact:6&detail=1', async tool => tool === 'get_goal_map' ? {
        memories: [{ memory_type: 'fact', memory_id: 6, content: '짧은 요약', content_truncated: true }]
    } : [{ id: 6, content: '목록에 포함된 전체 내용' }]);
    await state.loadRoute();
    assert.equal(state.selected.item.content, '목록에 포함된 전체 내용');
    assert.equal(state.selected.item.content_truncated, false);
    assert.equal(state.selected.kind, 'memory');
});

test('MCP errors and oversized responses are never treated as empty success', async () => {
    const api = createApiClient();
    assert.throws(() => api.toolValue({ result: { isError: true, content: [{ type: 'text', text: '권한이 없습니다' }] } }), /권한/);
    assert.throws(() => api.toolValue({ error: { message: '요청 오류' } }), /요청 오류/);
    const { state } = setup('/', async () => ({ result_omitted: true }));
    await state.loadRoute();
    assert.match(state.t(state.error), /데이터가 많아/);
    assert.equal(state.mapLoaded, false);
});

test('expired work remains searchable on the map and canvas fitting is bounded', () => {
    const expired = { id: 2, status: 'doing', attempt_status: 'active', lease_expires_at: '2000-01-01' };
    const filtered = goalMap.filterGoalMap({ work_items: [expired] }, { status: 'expired' });
    assert.equal(filtered.work_items.length, 1);
    const { state } = setup();
    state.viewportWidth = 400;
    assert.equal(state.canvasStyle({ width: 1000, height: 800 }).zoom, 0.4);
    state.fitMap = false;
    assert.equal(state.canvasStyle({ width: 1000, height: 800 }).zoom, 1);
});

test('map pages, source links and server-calculated component progress reach the view', async () => {
    const { state } = setup('/ui/goal-map', async (_, args) => args.offset ? {
        snapshot: 'same', items: [{ kind: 'unassigned_work', value: { id: 8, status: 'ready', execution_progress: { status: 'done', total: 2, done: 2, canceled: 0 } } }], has_more: false
    } : { snapshot: 'same', root_goal_id: 1, items: [{ kind: 'goal', value: { id: 1, content: '목표' } }], has_more: true, next_offset: 1 });
    await state.loadRoute();
    assert.equal(state.map.goal_tree.goals.length, 1);
    assert.equal(state.filteredMap.work_items.length, 1);
    assert.equal(state.displayStatus(state.allWork[0]), 'done');
    assert.match(state.workNote(state.allWork[0]), /2개 중 2개 완료/);
});

test('memory originals page by type and stale detail requests cannot overwrite a new selection', async () => {
    let release;
    const { state } = setup('/', async (_, args) => {
        if (args.memory_id === 9) return new Promise(resolve => { release = resolve; });
        return args.offset ? { content: '뒷부분', snapshot: 'v1', has_more: false, status: 'active' } : { content: '앞부분', snapshot: 'v1', has_more: true, next_offset: 3, status: 'active' };
    });
    state.selectObject('memory', { memory_type: 'fact', memory_id: 8, content: '요약', content_truncated: true });
    await state.openDetail();
    assert.equal(state.selected.item.content, '앞부분뒷부분');
    assert.equal(state.selected.item.content_truncated, false);
    state.selectObject('memory', { memory_type: 'fact', memory_id: 9 });
    const pending = state.openDetail();
    state.selectObject('work', { id: 4, title: '다른 작업' });
    release({ content: '늦은 원문', snapshot: 'old' });
    await pending;
    assert.equal(state.selected.kind, 'work');
    assert.equal(state.selected.item.title, '다른 작업');
});

test('maintenance uses the existing write endpoint and canceled reindex sends no request', async () => {
    const { state, api, window } = setup('/ui/maintenance');
    const calls = [];
    api.adminRequest = async (path, options) => { calls.push({ path, options }); return options ? { woken: 2, status: { pending: 2 } } : { pending: 0 }; };
    await state.loadRoute();
    await state.runMaintenance('retry');
    assert.equal(calls[1].options.method, 'POST');
    assert.equal(state.maintenance.pending, 2);
    assert.match(state.t(state.maintenanceNotice), /2개/);
    window.confirm = () => false;
    await state.runMaintenance('reindex');
    assert.equal(calls.length, 2);
});

test('ticketless memory browsing preserves type, scope, and original-memory IDs', async () => {
    const calls = [];
    const { state, window } = setup('/ui/memories?namespace=/personal&memory=episode', async (tool, args) => {
        calls.push({ tool, args });
        return tool === 'get_goal_map' ? {} : [{ memory_type: 'episode', memory_id: 7, content: '개인 기록' }];
    });
    await state.loadRoute();
    assert.equal(state.isListRoute, true);
    assert.equal(calls.at(-1).tool, 'list_memories');
    assert.equal(calls.at(-1).args.namespace, '/personal');
    assert.equal(calls.at(-1).args.memory_type, 'episode');
    assert.deepEqual(state.statusOptions, []);
    state.selectListItem(state.listItems[0]);
    assert.equal(state.selected.key, 'memory:episode:7');
    state.syncURL();
    assert.equal(routeAPI.readRoute(window.location).memoryType, 'episode');
});

test('a standalone fact detail connects its original episode without a work item', async () => {
    const fact = { key: 'memory:fact:1', memory_type: 'fact', memory_id: 1, content: '사실' };
    const episode = { key: 'memory:episode:2', memory_type: 'episode', memory_id: 2, content: '원본 경험' };
    const { state } = setup('/ui/memories?namespace=/personal', async tool => {
        if (tool === 'get_memory') return { content: '사실 전체', snapshot: 'original', has_more: false };
        if (tool === 'get_memory_context') return { memories: [fact, episode], edges: [{ key: 'source', from: fact.key, to: episode.key, relation: 'derived_from', derived: true }] };
        return {};
    });
    state.selectObject('memory', fact);
    await state.openDetail();
    assert.equal(state.selected.item.content, '사실 전체');
    assert.equal(state.selectedConnections[0].item.content, '원본 경험');
    assert.match(state.selectedConnections[0].label, /원본 경험/);
});

test('language changes presentation and persists without refetching or losing work state', async () => {
    let requests = 0;
    const { state, window } = setup('/ui/goal-map?namespace=/projects/demo', async () => { requests++; return map; });
    const stored = new Map();
    window.localStorage = { getItem: key => stored.get(key), setItem: (key, value) => stored.set(key, value) };
    await state.loadRoute();
    state.filters.query = '원본';
    state.selectObject('memory', { id: 3, memory_type: 'fact', content: '번역하면 안 되는 원본' });
    state.route.detail = true;
    state.syncURL();
    state.error = 'error.network';
    state.maintenanceNotice = { key: 'maintenance.queued', params: { count: 2 } };
    const before = JSON.stringify({ route: state.route, filters: state.filters, selected: state.selected });
    const url = window.location.href;
    const count = requests;
    state.changeLocale('en');
    assert.equal(state.pageTitle, 'Overview');
    assert.equal(state.selectedTitle, 'Fact #3');
    assert.equal(state.selectedFields.find(field => field.label === 'Content').value, '번역하면 안 되는 원본');
    assert.equal(state.navItems.find(item => item.route === 'graph').label, 'Work flow');
    assert.equal(state.t(state.maintenanceNotice), '2 items queued.');
    assert.equal(state.t(state.error), 'Could not connect to the server.');
    assert.match(state.agentGuide, /^# Stash Work Plan Convention/);
    assert.equal(window.document.documentElement.lang, 'en');
    assert.equal(window.document.title, 'Overview · Stash');
    assert.equal(window.location.href, url);
    assert.equal(JSON.stringify({ route: state.route, filters: state.filters, selected: state.selected }), before);
    assert.equal(requests, count);
    const fresh = createViewModel({ api: {}, routeAPI, goalMap, workGraph, search, window }).data();
    assert.equal(fresh.locale, 'en');
    state.changeLocale('ko');
    assert.equal(state.selectedTitle, '사실 #3');
    assert.match(state.agentGuide, /^# Stash 작업 관리 규칙/);
    assert.equal(state.navItems.find(item => item.route === 'graph').label, '작업 흐름');
    window.localStorage.setItem = () => { throw new Error('storage denied'); };
    state.changeLocale('en');
    assert.equal(state.locale, 'en');
});
