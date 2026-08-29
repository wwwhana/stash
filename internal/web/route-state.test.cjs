const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { routePaths, readRoute, buildRoute, routeTitle } = require('./ui/route-state.js');

test('every workspace page has a stable address', () => {
    assert.deepEqual(routePaths, {
        'goal-map': '/ui/goal-map', plan: '/ui/plan', board: '/ui/issues', graph: '/ui/work-graph',
        worktrees: '/ui/git', list_namespaces: '/ui/namespaces', query_facts: '/ui/facts',
        list_hypotheses: '/ui/hypotheses', list_goals: '/ui/goals', agent: '/ui/agent-guide',
        maintenance: '/ui/maintenance'
    });
    assert.equal(routeTitle('plan'), '작업 계획');
});

test('work graph address restores namespace and filters', () => {
    const href = buildRoute('graph', {
        namespace: '/projects/agent-atlas-demo', query: 'Confluence', status: 'doing'
    });
    assert.equal(href, '/ui/work-graph?namespace=%2Fprojects%2Fagent-atlas-demo&q=Confluence&status=doing');
    assert.deepEqual(readRoute('http://stash.local' + href), {
        route: 'graph', matched: true, namespace: '/projects/agent-atlas-demo', project: '',
        query: 'Confluence', status: 'doing', agent: '', memoryType: '',
        kinds: { goal: true, work: true, memory: true, resource: true },
        issueType: '', label: '', offset: 0, issueID: 0
    });
});

test('goal map address restores hidden node kinds', () => {
    const href = buildRoute('goal-map', {
        namespace: '/projects/demo', query: '근거', memoryType: 'fact',
        kinds: { goal: true, work: true, memory: false, resource: false }
    });
    const route = readRoute(href);
    assert.equal(route.route, 'goal-map');
    assert.equal(route.namespace, '/projects/demo');
    assert.equal(route.query, '근거');
    assert.equal(route.memoryType, 'fact');
    assert.deepEqual(route.kinds, { goal: true, work: true, memory: false, resource: false });
});

test('project plan and issue drawer keep only their own query state', () => {
    assert.equal(buildRoute('plan', { project: '/projects/demo', namespace: '/self', query: 'ignored' }), '/ui/plan?project=%2Fprojects%2Fdemo');
    assert.equal(buildRoute('board', { query: '로그인', issueType: 'bug', label: 'urgent', offset: 50, issueID: 17 }), '/ui/issues?q=%EB%A1%9C%EA%B7%B8%EC%9D%B8&type=bug&label=urgent&offset=50&issue=17');
    assert.equal(buildRoute('graph', { namespace: '/projects/demo', issueID: 17 }), '/ui/work-graph?namespace=%2Fprojects%2Fdemo&issue=17');
});

test('root and unknown paths safely select the goal map', () => {
    assert.deepEqual(
        { route: readRoute('/').route, matched: readRoute('/').matched },
        { route: 'goal-map', matched: true }
    );
    assert.deepEqual(
        { route: readRoute('/missing').route, matched: readRoute('/missing').matched },
        { route: 'goal-map', matched: false }
    );
});

test('the console restores routes and exposes real navigation links', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const viewModel = html.match(/function createRouteViewModel\(\) \{[\s\S]*?\n        function createMapScopeViewModel/)?.[0] || '';

    assert.match(html, /<script defer src="\/route-state\.js"><\/script>/);
    assert.match(html, /<a :href="routeHref\('plan'\)" @click\.prevent="loadWorkPlan\(\)"/);
    assert.match(html, /<a :href="routeHref\('graph'\)" @click\.prevent="loadWorkGraph\(\)"/);
    assert.match(viewModel, /window\.history\[replace \? 'replaceState' : 'pushState'\]/);
    assert.match(viewModel, /async restoreRoute\(\)/);
    assert.match(html, /window\.addEventListener\('popstate'/);
    assert.match(html, /await this\.restoreRoute\(\)/);
});
