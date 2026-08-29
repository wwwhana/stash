const test = require('node:test');
const assert = require('node:assert/strict');
const { buildGoalMapLayout } = require('./ui/goal-map-layout.js');

function sampleMap() {
    return {
        goal_tree: { root_goal_id: 1, goals: [
            { id: 1, content: 'A', depth: 0, progress: 0.5 },
            { id: 2, parent_id: 1, content: 'A-1', depth: 1, progress: 1 },
            { id: 3, parent_id: 1, content: 'A-2', depth: 1, progress: 0 }
        ] },
        work_items: [{ id: 10, goal_id: 2, issue_key: 'W-10', title: 'A-1 작업', status: 'done' }],
        memories: [{ key: 'memory:fact:5', memory_type: 'fact', memory_id: 5, content: 'A의 제약' }],
        edges: [
            { key: 'm-w', from: 'memory:fact:5', to: 'work:10', relation: 'constraint' },
            { key: 'w-g', from: 'work:10', to: 'goal:2', relation: 'contributes_to' },
            { key: 'g-g', from: 'goal:2', to: 'goal:1', relation: 'contributes_to' }
        ]
    };
}

test('memory and child outcomes flow toward the shared root', () => {
    const layout = buildGoalMapLayout(sampleMap());
    const byKey = new Map(layout.nodes.map(node => [node.key, node]));
    assert.ok(byKey.get('memory:fact:5').x < byKey.get('work:10').x);
    assert.ok(byKey.get('work:10').x < byKey.get('goal:2').x);
    assert.ok(byKey.get('goal:2').x < byKey.get('goal:1').x);
    assert.equal(layout.edges.length, 3);
    assert.equal(layout.columns.at(-1).label, '공통 목표');
});

test('unknown endpoints are ignored without dropping valid nodes', () => {
    const value = sampleMap();
    value.edges.push({ key: 'missing', from: 'work:404', to: 'goal:1', relation: 'contributes_to' });
    const layout = buildGoalMapLayout(value);
    assert.equal(layout.nodes.length, 5);
    assert.equal(layout.edges.length, 3);
});

test('empty maps have a stable empty layout', () => {
    assert.deepEqual(buildGoalMapLayout({}), {
        width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], columns: [], counts: { memory: 0, work: 0, goal: 0 }
    });
});
