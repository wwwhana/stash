const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { buildGoalMapLayout } = require('./ui/goal-map-layout.js');

function sampleMap() {
    return {
        goal_tree: { root_goal_id: 1, goals: [
            { id: 1, content: 'A', depth: 0, progress: 0.5 },
            { id: 2, parent_id: 1, content: 'A-1', depth: 1, progress: 1 },
            { id: 3, parent_id: 1, content: 'A-2', depth: 1, progress: 0 }
        ] },
        work_items: [{ id: 10, goal_id: 2, issue_key: 'W-10', title: 'A-1 작업', status: 'done' }],
        resources: [{ key: 'resource:8', id: 8, kind: 'ticket', source: 'jira', authority: 'external', title: '사람 작업' }],
        memories: [{ key: 'memory:fact:5', memory_type: 'fact', memory_id: 5, content: 'A의 제약' }],
        edges: [
            { key: 'r-w', from: 'resource:8', to: 'work:10', relation: 'input' },
            { key: 'm-w', from: 'memory:fact:5', to: 'work:10', relation: 'constraint' },
            { key: 'w-g', from: 'work:10', to: 'goal:2', relation: 'contributes_to' },
            { key: 'g-g', from: 'goal:2', to: 'goal:1', relation: 'contributes_to' }
        ]
    };
}

test('memory and child outcomes flow toward the shared root', () => {
    const layout = buildGoalMapLayout(sampleMap());
    const byKey = new Map(layout.nodes.map(node => [node.key, node]));
    assert.ok(byKey.get('resource:8').x < byKey.get('work:10').x);
    assert.ok(byKey.get('memory:fact:5').x < byKey.get('work:10').x);
    assert.ok(byKey.get('work:10').x < byKey.get('goal:2').x);
    assert.ok(byKey.get('goal:2').x < byKey.get('goal:1').x);
    assert.equal(layout.edges.length, 4);
    assert.equal(layout.columns[0].label, '연결 자료');
    assert.equal(layout.columns.at(-1).label, '공통 목표');
});

test('unknown endpoints are ignored without dropping valid nodes', () => {
    const value = sampleMap();
    value.edges.push({ key: 'missing', from: 'work:404', to: 'goal:1', relation: 'contributes_to' });
    const layout = buildGoalMapLayout(value);
    assert.equal(layout.nodes.length, 6);
    assert.equal(layout.edges.length, 4);
});

test('empty maps have a stable empty layout', () => {
    assert.deepEqual(buildGoalMapLayout({}), {
        width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], columns: [], counts: { resource: 0, memory: 0, work: 0, goal: 0 }
    });
});

test('goal map UI keeps resource and monitoring state in its own view-model', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const viewModel = html.match(/function createGoalMapViewModel\(\) \{[\s\S]*?\n        function createPlanViewModel/)?.[0] || '';

    assert.match(viewModel, /resources: \[\]/);
    assert.match(viewModel, /goalMapAttentionItems\(\)/);
    assert.match(viewModel, /required_capabilities/);
    assert.match(viewModel, /invokeTool\('get_goal_map'/);
    assert.match(html, /node\.kind === 'resource'/);
    assert.match(html, />연결 자료</);
    assert.match(html, /@submit\.prevent="claimWork"/);
    assert.match(html, /runExecutionMutation\('claim_work'/);
    assert.match(html, /@media \(max-width: 680px\)[\s\S]*?\.stash-goal-map__summary \{ flex-wrap: wrap; \}/);
});
