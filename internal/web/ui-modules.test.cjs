const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');

const { createStateStore } = require('./ui/state-store.js');
const { createApiClient } = require('./ui/api-client.js');
const { createRouteViewModel } = require('./ui/route-view-model.js');
const { createMapScopeViewModel } = require('./ui/map-scope-view-model.js');
const { createWorkGraphViewModel } = require('./ui/work-graph-view-model.js');
const { createGoalMapViewModel } = require('./ui/goal-map-view-model.js');
const { createWorkPlanViewModel } = require('./ui/work-plan-view-model.js');
const { createIssueExecutionViewModel } = require('./ui/issue-execution-view-model.js');
const { createConsoleViewModel } = require('./ui/console-app.js');

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
        '/work-graph-view-model.js',
        '/goal-map-view-model.js',
        '/work-plan-view-model.js',
        '/issue-execution-view-model.js',
        '/console-app.js',
        '/alpine-3.16.3.min.js'
    ]);
    assert.doesNotMatch(html, /<script>/);
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
    const goalMap = createGoalMapViewModel();
    const plan = createWorkPlanViewModel();
    const execution = createIssueExecutionViewModel();

    assert.equal(typeof route.restoreRoute, 'function');
    assert.equal(typeof scope.loadMapNamespaces, 'function');
    assert.equal(typeof graph.loadWorkGraph, 'function');
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
    assert.equal(typeof first.loadGoalMap, 'function');
    assert.equal(typeof first.loadWorkPlan, 'function');
    assert.equal(typeof first.finishWork, 'function');
    assert.notStrictEqual(first.graph, second.graph);
    assert.notStrictEqual(first.workExecution, second.workExecution);
});
