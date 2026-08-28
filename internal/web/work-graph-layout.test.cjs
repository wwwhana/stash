const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { buildWorkGraphLayout } = require('./ui/work-graph-layout.js');

function node(id, status = 'ready', position = 0) {
    return { id, issue_key: id, title: `Task ${id}`, status, position };
}

function edge(id, from, to, edgeType = 'blocks') {
    return { id, from_item_id: from, to_item_id: to, edge_type: edgeType };
}

function placed(layout, id) {
    return layout.nodes.find(item => item.item.id === id);
}

function childPlaced(parent, id) {
    return parent.childLayout.nodes.find(item => item.item.id === id);
}

test('fork and join use longest predecessor depth', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'C', 'D'].map(id => node(id)),
        [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')]
    );

    assert.equal(placed(layout, 'A').depth, 0);
    assert.equal(placed(layout, 'B').depth, 1);
    assert.equal(placed(layout, 'C').depth, 1);
    assert.equal(placed(layout, 'B').x, placed(layout, 'C').x);
    assert.equal(placed(layout, 'D').depth, 2);
    assert.ok(placed(layout, 'D').x > placed(layout, 'B').x);
    assert.equal(layout.edges.filter(item => item.edge.to_item_id === 'D').length, 2);
});

test('status and board position never control graph placement', () => {
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C')];
    const before = buildWorkGraphLayout([
        node('A', 'ready', 1), node('B', 'ready', 2), node('C', 'doing', 3)
    ], edges);
    const after = buildWorkGraphLayout([
        node('A', 'doing', 99), node('B', 'blocked', -10), node('C', 'done', 0)
    ], edges);

    for (const id of ['A', 'B', 'C']) {
        assert.deepEqual(
            { x: placed(after, id).x, y: placed(after, id).y, depth: placed(after, id).depth },
            { x: placed(before, id).x, y: placed(before, id).y, depth: placed(before, id).depth }
        );
    }
});

test('session offset moves a root node, updates its blocks path, expands the canvas, and resets', () => {
    const nodes = ['A', 'B'].map(id => node(id));
    const edges = [edge(1, 'A', 'B')];
    const initial = buildWorkGraphLayout(nodes, edges);
    const moved = buildWorkGraphLayout(nodes, edges, { offsets: { 'root:B': { x: 180, y: 120 } } });
    const reset = buildWorkGraphLayout(nodes, edges, { offsets: {} });

    assert.equal(placed(moved, 'B').x, placed(initial, 'B').x + 180);
    assert.equal(placed(moved, 'B').y, placed(initial, 'B').y + 120);
    assert.equal(placed(moved, 'B').depth, placed(initial, 'B').depth);
    assert.notEqual(moved.edges[0].path, initial.edges[0].path);
    assert.equal(moved.width, initial.width + 180);
    assert.equal(moved.height, initial.height + 120);
    assert.deepEqual(
        { x: placed(reset, 'B').x, y: placed(reset, 'B').y, path: reset.edges[0].path, width: reset.width, height: reset.height },
        { x: placed(initial, 'B').x, y: placed(initial, 'B').y, path: initial.edges[0].path, width: initial.width, height: initial.height }
    );
});

test('relates_to is metadata and does not add dependency depth', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'C'].map(id => node(id)),
        [edge(1, 'A', 'B', 'relates_to'), edge(2, 'B', 'C')]
    );

    assert.equal(placed(layout, 'A').depth, 0);
    assert.equal(placed(layout, 'B').depth, 0);
    assert.equal(placed(layout, 'C').depth, 1);
    assert.equal(layout.edges.find(item => item.edge.id === 1).dashArray, '7 6');
});

test('items without any valid edge are kept in a separate area', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'E'].map(id => node(id)),
        [edge(1, 'A', 'B')]
    );

    assert.deepEqual(layout.disconnected.map(item => item.item.id), ['E']);
    assert.equal(placed(layout, 'E'), undefined);
});

test('strongly connected blocks edges are grouped and marked as a cycle', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'C'].map(id => node(id)),
        [edge(1, 'A', 'B'), edge(2, 'B', 'A'), edge(3, 'B', 'C')]
    );

    assert.equal(layout.cycles.length, 1);
    assert.deepEqual(layout.cycles[0].nodeIds, ['A', 'B']);
    assert.equal(placed(layout, 'A').depth, placed(layout, 'B').depth);
    assert.ok(placed(layout, 'A').cycle);
    assert.equal(placed(layout, 'C').depth, placed(layout, 'B').depth + 1);
    assert.equal(layout.edges.filter(item => item.cycle).length, 2);
});

test('nested parent_id items stay inside their top-level parent card', () => {
    const nodes = [
        node('P'),
        { ...node('C'), parent_id: 'P' },
        { ...node('G'), parent_id: 'C' }
    ];
    const layout = buildWorkGraphLayout(nodes, [], { expandedIds: ['P'] });
    const parent = placed(layout, 'P');

    assert.deepEqual(layout.nodes.map(item => item.item.id), ['P']);
    assert.deepEqual(parent.descendants.map(item => item.id), ['C', 'G']);
    assert.equal(parent.expanded, true);
    assert.equal(parent.childLayout.nodes.length, 0);
    const grandchild = parent.childLayout.disconnected.find(item => item.item.id === 'G');
    assert.equal(grandchild.hierarchyDepth, 2);
    assert.equal(grandchild.parentItem.id, 'C');
});

test('child fork and join keep parallel depth inside the expanded parent', () => {
    const nodes = [node('P'), ...['A', 'B', 'C', 'D'].map(id => ({ ...node(id), parent_id: 'P' }))];
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')];
    const layout = buildWorkGraphLayout(nodes, edges, { expandedIds: ['P'] });
    const parent = placed(layout, 'P');

    assert.equal(layout.nodes.length, 1);
    assert.equal(childPlaced(parent, 'A').depth, 0);
    assert.equal(childPlaced(parent, 'B').depth, 1);
    assert.equal(childPlaced(parent, 'C').depth, 1);
    assert.equal(childPlaced(parent, 'B').x, childPlaced(parent, 'C').x);
    assert.equal(childPlaced(parent, 'D').depth, 2);
    assert.equal(parent.childLayout.edges.filter(item => item.edge.to_item_id === 'D').length, 2);
});

test('session offset moves a child join and keeps every incoming edge attached', () => {
    const nodes = [node('P'), ...['A', 'B', 'C', 'D'].map(id => ({ ...node(id), parent_id: 'P' }))];
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')];
    const initialParent = placed(buildWorkGraphLayout(nodes, edges, { expandedIds: ['P'] }), 'P');
    const movedParent = placed(buildWorkGraphLayout(nodes, edges, {
        expandedIds: ['P'],
        offsets: { 'child:D': { x: 90, y: 70 } }
    }), 'P');

    assert.equal(childPlaced(movedParent, 'D').x, childPlaced(initialParent, 'D').x + 90);
    assert.equal(childPlaced(movedParent, 'D').y, childPlaced(initialParent, 'D').y + 70);
    assert.equal(movedParent.childLayout.width, initialParent.childLayout.width + 90);
    assert.equal(movedParent.childLayout.height, initialParent.childLayout.height + 70);
    const initialIncoming = initialParent.childLayout.edges.filter(item => item.edge.to_item_id === 'D').map(item => item.path);
    const movedIncoming = movedParent.childLayout.edges.filter(item => item.edge.to_item_id === 'D').map(item => item.path);
    assert.equal(movedIncoming.length, 2);
    assert.notDeepEqual(movedIncoming, initialIncoming);
});

test('orphan parent_id keeps the item visible and reports the missing parent', () => {
    const orphan = { ...node('X'), parent_id: 999 };
    const layout = buildWorkGraphLayout([orphan], []);

    assert.equal(layout.sourceNodeCount, 1);
    assert.equal(layout.nodes.length, 0);
    assert.equal(layout.disconnected.length, 1);
    assert.equal(layout.disconnected[0].item.id, 'X');
    assert.equal(layout.disconnected[0].orphanParentId, 999);
    assert.match(layout.hierarchyWarnings[0].label, /#999/);
});

test('cross-parent child dependency advances parent depth and stays named', () => {
    const nodes = [
        node('P'), node('Q'),
        { ...node('A'), parent_id: 'P' },
        { ...node('B'), parent_id: 'Q' }
    ];
    const layout = buildWorkGraphLayout(nodes, [edge(1, 'A', 'B')]);

    assert.equal(placed(layout, 'P').depth, 0);
    assert.equal(placed(layout, 'Q').depth, 1);
    assert.equal(layout.crossParentLinks.length, 1);
    assert.equal(layout.crossParentLinks[0].label, 'A → B');
    assert.equal(placed(layout, 'A'), undefined);
    assert.equal(placed(layout, 'B'), undefined);
});

test('disconnected parent and child cards keep status classes and labels', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const childArea = html.match(/<section x-show="node\.childLayout\.disconnected\.length"[\s\S]*?<\/section>/)?.[0] || '';
    const rootArea = html.match(/<section x-show="workGraphLayout\.disconnected\.length"[\s\S]*?<\/section>/)?.[0] || '';

    assert.match(childArea, /:class="graphNodeClasses\(child\)"/);
    assert.match(childArea, /statusLabel\(child\.item\.status\)/);
    assert.match(rootArea, /:class="graphNodeClasses\(entry\)"/);
    assert.match(rootArea, /statusLabel\(entry\.item\.status\)/);
});

test('drag handles stay in the graph view-model and never persist offsets', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const graphViewModel = html.match(/function createWorkGraphViewModel\(\) \{[\s\S]*?\n        function createPlanViewModel/)?.[0] || '';

    assert.match(graphViewModel, /graphNodeOffsets: \{\}/);
    assert.match(graphViewModel, /startGraphNodeDrag\(/);
    assert.match(graphViewModel, /moveGraphNodeWithKeyboard\(/);
    assert.match(graphViewModel, /resetGraphLayout\(/);
    assert.match(graphViewModel, /return \{ minX: 0, minY: 0 \}/);
    assert.doesNotMatch(graphViewModel, /localStorage|sessionStorage/);
    assert.doesNotMatch(graphViewModel, /invokeTool\([^)]*graphNodeOffsets/);
    assert.match(html, /data-graph-drag-key/);
    assert.match(html, />배치 초기화<\/button>/);
});

test('graph shell uses the full viewport and keeps dependency stages compact', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const nodes = [node('P'), ...['A', 'B', 'C', 'D'].map(id => ({ ...node(id), parent_id: 'P' }))];
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')];
    const parent = placed(buildWorkGraphLayout(nodes, edges, { expandedIds: ['P'] }), 'P');
    const singleRow = buildWorkGraphLayout(
        ['A', 'B', 'C'].map(id => node(id)),
        [edge(5, 'A', 'B'), edge(6, 'B', 'C')]
    );

    assert.match(html, /\.stash-main \{[^}]*width: 100% !important;[^}]*max-width: none !important;/);
    assert.match(html, /@media \(min-width: 981px\) \{[\s\S]*?\.stash-main \{ padding-right: 0 !important; \}/);
    assert.match(html, /\.stash-graph \{[^}]*height: 100%;[^}]*min-height: 100%;[^}]*display: flex;[^}]*flex-direction: column;/);
    assert.match(html, /\.stash-graph-content \{[^}]*flex: 1 1 auto;[^}]*flex-direction: column;/);
    assert.match(html, /class="stash-graph-content"/);
    assert.doesNotMatch(html, /<main class="[^"]*max-w-7xl/);
    assert.ok(parent.childLayout.width <= 470, `child flow width ${parent.childLayout.width}px`);
    assert.equal(singleRow.height, 200);
});
