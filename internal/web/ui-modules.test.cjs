const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const { createStateStore } = require('./ui/state-store.js');
const { createApiClient } = require('./ui/api-client.js');
const { createRouteViewModel } = require('./ui/route-view-model.js');
const { createMapScopeViewModel } = require('./ui/map-scope-view-model.js');
const { createWorkGraphViewModel } = require('./ui/work-graph-view-model.js');
const { createGraphViewportViewModel } = require('./ui/graph-viewport-view-model.js');
const { createWorkMonitorViewModel } = require('./ui/work-monitor-view-model.js');
const { createProjectMonitorViewModel } = require('./ui/project-monitor-view-model.js');
const { createGoalMapViewModel } = require('./ui/goal-map-view-model.js');
const { createWorkPlanViewModel } = require('./ui/work-plan-view-model.js');
const { createIssueExecutionViewModel } = require('./ui/issue-execution-view-model.js');
const { composeViewModels, createConsoleViewModel } = require('./ui/console-app.js');

test('the HTML loads every view-model before Alpine starts', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const scripts = Array.from(html.matchAll(/<script defer src="([^"]+)"><\/script>/g), match => match[1]);

    assert.deepEqual(scripts, [
        '/work-graph-layout.js',
        '/goal-map-layout.js',
        '/route-state.js',
        '/state-store.js',
        '/api-client.js',
        '/route-view-model.js',
        '/map-scope-view-model.js',
        '/work-board-scope-view-model.js',
        '/work-graph-view-model.js',
        '/graph-viewport-view-model.js',
        '/work-monitor-view-model.js',
        '/project-monitor-view-model.js',
        '/goal-map-view-model.js',
        '/work-plan-view-model.js',
        '/issue-execution-view-model.js',
        '/console-app.js',
        '/alpine-3.16.3.min.js'
    ]);
    assert.doesNotMatch(html, /<script>/);
    assert.match(html, /<link rel="stylesheet" href="\/work-graph-board\.css">/);
});

test('the common state store returns isolated mutable state', () => {
    const first = createStateStore();
    const second = createStateStore();

    first.boardFilter.q = '첫 화면';
    first.boardPage.history.push(50);
    assert.equal(second.boardFilter.q, '');
    assert.deepEqual(second.boardPage.history, []);
    assert.notStrictEqual(first.notice, second.notice);
    assert.notStrictEqual(first.agentGuide, '');
});

test('the common API module owns HTTP and MCP transport helpers', () => {
    const api = createApiClient();

    for (const method of ['adminRequest', 'invokeTool', 'toolValue', 'pageSlice', 'initializeSession', 'sendMCPRequest']) {
        assert.equal(typeof api[method], 'function', method);
    }
});

test('each screen factory owns its state and actions', () => {
    const route = createRouteViewModel();
    const scope = createMapScopeViewModel();
    const graph = createWorkGraphViewModel();
    const viewport = createGraphViewportViewModel();
    const monitor = createWorkMonitorViewModel();
    const projectMonitor = createProjectMonitorViewModel();
    const goalMap = createGoalMapViewModel();
    const plan = createWorkPlanViewModel();
    const execution = createIssueExecutionViewModel();

    assert.equal(typeof route.restoreRoute, 'function');
    assert.equal(typeof scope.loadMapNamespaces, 'function');
    assert.equal(typeof graph.loadWorkGraph, 'function');
    assert.equal(typeof viewport.fitGraphViewport, 'function');
    assert.equal(typeof monitor.loadWorkMonitor, 'function');
    assert.equal(typeof projectMonitor.loadProjectMonitor, 'function');
    assert.equal(typeof goalMap.loadGoalMap, 'function');
    assert.equal(typeof plan.loadWorkPlan, 'function');
    assert.equal(typeof execution.finishWork, 'function');
});

test('the console composes modules into one Alpine view-model without shared state', () => {
    const first = createConsoleViewModel();
    const second = createConsoleViewModel();

    assert.equal(first.view, 'goal-map');
    assert.equal(typeof first.invokeTool, 'function');
    assert.equal(typeof first.restoreRoute, 'function');
    assert.equal(typeof first.loadWorkGraph, 'function');
    assert.equal(typeof first.fitGraphViewport, 'function');
    assert.equal(typeof first.loadWorkMonitor, 'function');
    assert.equal(typeof first.loadProjectMonitor, 'function');
    assert.equal(typeof first.loadGoalMap, 'function');
    assert.equal(typeof first.loadWorkPlan, 'function');
    assert.equal(typeof first.finishWork, 'function');
    assert.notStrictEqual(first.graph, second.graph);
    first.graphProjectSlug = '/projects/first';
    assert.equal(second.graphProjectSlug, '');
    first.projectMonitorFilter.status = 'doing';
    assert.equal(second.projectMonitorFilter.status, '');
    assert.notStrictEqual(first.workExecution, second.workExecution);
});

test('ViewModel composition rejects duplicate keys and names both owners', () => {
    assert.throws(
        () => composeViewModels([
            { name: 'routeViewModel', value: { restoreRoute() {} } },
            { name: 'consoleViewModel', value: { restoreRoute() {} } }
        ]),
        /Duplicate ViewModel key "restoreRoute" from "routeViewModel" and "consoleViewModel"\./
    );
});

test('the browser UMD path exposes the graph viewport through stashConsole', () => {
    const viewportSource = fs.readFileSync(require.resolve('./ui/graph-viewport-view-model.js'), 'utf8');
    const consoleSource = fs.readFileSync(require.resolve('./ui/console-app.js'), 'utf8');
    const browser = {};
    vm.createContext(browser);
    vm.runInContext(viewportSource, browser);
    Object.assign(browser, {
        StashStateStore: { createStateStore: () => ({}) },
        StashApiClient: { createApiClient: () => ({}) },
        StashRouteViewModel: { createRouteViewModel: () => ({}) },
        StashWorkPlanViewModel: { createWorkPlanViewModel: () => ({}) },
        StashIssueExecutionViewModel: { createIssueExecutionViewModel: () => ({}) },
        StashMapScopeViewModel: { createMapScopeViewModel: () => ({}) },
        StashWorkBoardScopeViewModel: { createWorkBoardScopeViewModel: () => ({}) },
        StashWorkGraphViewModel: { createWorkGraphViewModel: () => ({}) },
        StashWorkMonitorViewModel: { createWorkMonitorViewModel: () => ({}) },
        StashProjectMonitorViewModel: { createProjectMonitorViewModel: () => ({}) },
        StashGoalMapViewModel: { createGoalMapViewModel: () => ({}) }
    });
    vm.runInContext(consoleSource, browser);

    assert.equal(typeof browser.StashGraphViewportViewModel.createGraphViewportViewModel, 'function');
    assert.equal(typeof browser.StashConsoleApp.createConsoleViewModel, 'function');
    assert.equal(typeof browser.stashConsole, 'function');
    assert.equal(typeof browser.stashConsole().fitGraphViewport, 'function');
});

test('work graph integration scales pointer movement and centers a selected node', () => {
    const graph = createWorkGraphViewModel();
    let written = null;
    graph.graphDragState = {
        key: 'node:7', pointerId: 4, startX: 100, startY: 80,
        originX: 5, originY: -3, bounds: { minX: -100, minY: -100 }
    };
    graph.graphViewportScale = () => 0.5;
    graph.writeGraphNodeOffset = (...args) => { written = args; };
    graph.moveGraphNodeDrag({ pointerId: 4, clientX: 130, clientY: 100, cancelable: true, preventDefault() {} });
    assert.deepEqual(written, ['node:7', 65, 37, graph.graphDragState.bounds]);

    let centered = null;
    graph.$refs = { graphViewport: { clientWidth: 640, clientHeight: 360 } };
    graph.$nextTick = callback => callback();
    graph.centerGraphNodeByID = (key, viewport) => { centered = [key, viewport]; };
    graph.scrollGraphNodeIntoView('7');
    assert.deepEqual(centered, ['7', graph.$refs.graphViewport]);
});

test('loading the work graph fits the completed layout to the visible viewport', async () => {
    const graph = createWorkGraphViewModel();
    const target = { clientWidth: 640, clientHeight: 360 };
    const calls = [];
    Object.assign(graph, {
        $refs: { graphViewport: target },
        $nextTick(callback) { callback(); },
        async loadMapNamespaces() {},
        syncGraphProjectFromNamespace() {},
        async loadWorkView() { this.workGraphLayout = { width: 1200, height: 700, nodes: [] }; },
        scheduleGraphViewportFit(viewport, layout) { calls.push(['schedule-fit', viewport, layout]); }
    });

    await graph.loadWorkGraph(false);
    assert.deepEqual(calls, [['schedule-fit', target, graph.workGraphLayout]]);
});

test('an empty issue page still offers the previous page', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');

    assert.match(html, /x-show="!loading && !boardError && \(boardPage\.offset > 0 \|\| boardPage\.hasNext\)"/);
    assert.match(html, /boardPage\.offset > 0 \? '이 쪽에는 이슈가 없습니다\.'/);
});

test('only the newest route restore finalizes and rewrites the address', async () => {
    const route = createRouteViewModel();
    const pending = [];
    const synced = [];
    const previousWindow = global.window;
    global.window = { location: { href: 'http://stash/ui/issues', pathname: '/ui/issues', search: '' } };
    Object.assign(route, {
        boardFilter: {}, boardPage: { offset: 0, history: [] }, selectedIssue: null,
        async loadWorkBoard() { await new Promise(resolve => pending.push(resolve)); },
        syncRoute(replace) { synced.push(replace); }
    });

    try {
        const older = route.restoreRoute();
        const newer = route.restoreRoute();
        pending[1]();
        await newer;
        pending[0]();
        await older;
    } finally {
        global.window = previousWindow;
    }

    assert.deepEqual(synced, [true]);
    assert.equal(route.routeRestoring, false);
    assert.equal(route.routeRestoreGeneration, 2);
});

test('a failed list remains an error until a retry succeeds', async () => {
    const app = createConsoleViewModel();
    let attempts = 0;
    Object.assign(app, {
        listPage: { tool: 'query_facts', args: { namespaces: '/' }, offset: 0, nextOffset: 0, limit: 50, hasNext: false, history: [] },
        async invokeTool() {
            attempts += 1;
            if (attempts === 1) throw new Error('연결할 수 없습니다.');
            return { items: [] };
        },
        toolValue(value) { return value; },
        pageSlice(value) { return { isPage: true, items: value.items, hasMore: false, nextOffset: 0 }; },
        syncRoute() {}, setNotice() {}, markLoaded() {}
    });

    await app.loadListPage();
    assert.equal(app.listError, '연결할 수 없습니다.');
    assert.equal(app.resultValue, null);
    await app.loadListPage();
    assert.equal(app.listError, '');
    assert.deepEqual(app.resultValue, []);
});

test('namespace loading retries after failure and does not select the only project', async () => {
    const scope = createMapScopeViewModel();
    let attempts = 0;
    Object.assign(scope, {
        async invokeTool() {
            attempts += 1;
            if (attempts === 1) throw new Error('temporary');
            return [{ slug: '/projects/only', name: '하나' }];
        },
        toolValue(value) { return value; },
        pageSlice(value) { return { items: value, hasMore: false, nextOffset: value.length }; }
    });

    await scope.loadMapNamespaces(false);
    assert.equal(scope.mapNamespacesLoaded, false);
    await scope.loadMapNamespaces(false);
    assert.equal(attempts, 2);
    assert.equal(scope.mapNamespacesLoaded, true);
    assert.equal(scope.mapNamespaceSlug, '');
});

test('a missing issue never opens a placeholder detail', async () => {
    const app = createConsoleViewModel();
    let opened = false;
    Object.assign(app, {
        async invokeTool() { throw new Error('해당 이슈를 찾을 수 없습니다.'); },
        resetIssueExecution() {},
        executionFailureMessage(error) { return error.message; },
        openIssueDrawer() { opened = true; },
        syncRoute() {}, setNotice() {}
    });

    await app.openIssue(999999999);
    assert.equal(app.selectedIssue, null);
    assert.equal(opened, false);
});
