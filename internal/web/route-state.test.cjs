const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { routePaths, readRoute, buildRoute, routeTitle } = require('./ui/route-state.js');

test('every workspace page has a stable address', () => {
    assert.deepEqual(routePaths, {
        'goal-map': '/ui/goal-map', plan: '/ui/plan', monitor: '/ui/monitor', board: '/ui/issues', graph: '/ui/work-graph',
        worktrees: '/ui/git', list_namespaces: '/ui/namespaces', query_facts: '/ui/facts',
        list_hypotheses: '/ui/hypotheses', list_goals: '/ui/goals', agent: '/ui/agent-guide',
        maintenance: '/ui/maintenance'
    });
    assert.equal(routeTitle('plan'), '작업 계획');
    assert.equal(routeTitle('monitor'), '작업 관제');
});

test('work graph address restores namespace and filters', () => {
    const href = buildRoute('graph', {
        project: '/projects/agent-atlas-demo', namespace: '/projects/agent-atlas-demo/ingest',
        query: 'Confluence', status: 'doing', agent: 'codex',
        relations: { part_of: true, blocks: false, relates_to: false }, focus: '42'
    });
    assert.equal(href, '/ui/work-graph?project=%2Fprojects%2Fagent-atlas-demo&namespace=%2Fprojects%2Fagent-atlas-demo%2Fingest&q=Confluence&status=doing&agent=codex&hide_relation=blocks%2Crelates_to&focus=42');
    assert.deepEqual(readRoute('http://stash.local' + href), {
        route: 'graph', matched: true, namespace: '/projects/agent-atlas-demo/ingest', project: '/projects/agent-atlas-demo',
        query: 'Confluence', status: 'doing', agent: 'codex', memoryType: '',
        kinds: { goal: true, work: true, memory: true, resource: true },
        relations: { part_of: true, blocks: false, relates_to: false }, focus: '42', detail: false,
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
    assert.equal(buildRoute('plan', { namespace: '/self', query: 'ignored' }), '/ui/plan?namespace=%2Fself');
    assert.equal(buildRoute('board', { project: '/projects/demo', namespace: '/projects/demo/api', query: '로그인', issueType: 'bug', label: 'urgent', offset: 50, issueID: 17 }), '/ui/issues?project=%2Fprojects%2Fdemo&namespace=%2Fprojects%2Fdemo%2Fapi&q=%EB%A1%9C%EA%B7%B8%EC%9D%B8&type=bug&label=urgent&offset=50&issue=17');
    assert.equal(buildRoute('graph', { namespace: '/projects/demo', issueID: 17 }), '/ui/work-graph?namespace=%2Fprojects%2Fdemo&issue=17');
});

test('project monitor address restores project filters and focused work', () => {
    const href = buildRoute('monitor', {
        project: '/projects/demo', query: '문서 연결', status: 'doing', agent: 'codex', focus: 42, issueID: 17
    });
    assert.equal(href, '/ui/monitor?project=%2Fprojects%2Fdemo&q=%EB%AC%B8%EC%84%9C+%EC%97%B0%EA%B2%B0&status=doing&agent=codex&focus=42&issue=17');
    const route = readRoute(href);
    assert.equal(route.route, 'monitor');
    assert.equal(route.project, '/projects/demo');
    assert.equal(route.query, '문서 연결');
    assert.equal(route.status, 'doing');
    assert.equal(route.agent, 'codex');
    assert.equal(route.focus, '42');
    assert.equal(route.issueID, 17);
});

test('fact, hypothesis, and goal list addresses restore their search', () => {
    const href = buildRoute('list_hypotheses', { namespace: '/projects/demo', query: '지원 정책', status: 'testing', offset: 50 });
    assert.equal(href, '/ui/hypotheses?namespace=%2Fprojects%2Fdemo&q=%EC%A7%80%EC%9B%90+%EC%A0%95%EC%B1%85&status=testing&offset=50');
    const route = readRoute(href);
    assert.equal(route.query, '지원 정책');
    assert.equal(route.status, 'testing');
    assert.equal(route.offset, 50);
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
    const viewModel = fs.readFileSync(require.resolve('./ui/route-view-model.js'), 'utf8');
    const app = fs.readFileSync(require.resolve('./ui/console-app.js'), 'utf8');

    assert.match(html, /<script defer src="\/route-state\.js"><\/script>/);
    assert.match(html, /<script defer src="\/route-view-model\.js"><\/script>/);
    assert.match(html, /<script defer src="\/console-app\.js"><\/script>/);
    assert.match(html, /<a :href="routeHref\('plan'\)" @click\.prevent="loadWorkPlan\(\)"/);
    assert.match(html, /<a :href="routeHref\('monitor'\)" @click\.prevent="loadProjectMonitor\(\)"/);
    assert.match(html, /<a :href="routeHref\('graph'\)" @click\.prevent="loadWorkGraph\(\)"/);
    assert.match(viewModel, /window\.history\[replace \? 'replaceState' : 'pushState'\]/);
    assert.match(viewModel, /async restoreRoute\(\)/);
    assert.match(viewModel, /relations: this\.graphFilter\.relations/);
    assert.match(viewModel, /project: this\.graphProjectSlug/);
    assert.match(viewModel, /agent: this\.graphFilter\.agent/);
    assert.match(viewModel, /focus: this\.graphFocusedKey/);
    assert.match(viewModel, /await this\.focusGraphNodeByID\(route\.focus\)/);
    assert.match(app, /window\.addEventListener\('popstate'/);
    assert.match(app, /await this\.restoreRoute\(\)/);
});
