const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { performance } = require('node:perf_hooks');
const { buildWorkGraphLayout } = require('./ui/work-graph-layout.js');
const { createWorkGraphViewModel } = require('./ui/work-graph-view-model.js');

function node(id, status = 'ready', position = 0) {
    return { id, issue_key: id, title: `Task ${id}`, status, position };
}

function edge(id, from, to, edgeType = 'blocks') {
    return { id, from_item_id: from, to_item_id: to, edge_type: edgeType };
}

function placed(layout, id) {
    return layout.nodes.find(item => item.item.id === id);
}

function workGraphViewModel() {
    return {
        ...createWorkGraphViewModel(), loading: false, view: 'graph',
        syncRoute() {}, statusLabel(value) { return value; }, $nextTick() {}
    };
}

test('fork and join use the longest predecessor depth', () => {
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
    assert.equal(layout.edges.filter(item => item.toKey === 'D' && item.type === 'blocks').length, 2);
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

test('session offset moves one node, updates attached paths, expands the canvas, and resets', () => {
    const nodes = ['A', 'B'].map(id => node(id));
    const edges = [edge(1, 'A', 'B')];
    const initial = buildWorkGraphLayout(nodes, edges);
    const moved = buildWorkGraphLayout(nodes, edges, { offsets: { 'node:B': { x: 220, y: 260 } } });
    const reset = buildWorkGraphLayout(nodes, edges, { offsets: {} });

    assert.equal(placed(moved, 'B').x, placed(initial, 'B').x + 220);
    assert.equal(placed(moved, 'B').y, placed(initial, 'B').y + 260);
    assert.equal(placed(moved, 'B').depth, placed(initial, 'B').depth);
    assert.notEqual(moved.edges[0].path, initial.edges[0].path);
    assert.ok(moved.width > initial.width);
    assert.ok(moved.height > initial.height);
    assert.deepEqual(
        { x: placed(reset, 'B').x, y: placed(reset, 'B').y, path: reset.edges[0].path, width: reset.width, height: reset.height },
        { x: placed(initial, 'B').x, y: placed(initial, 'B').y, path: initial.edges[0].path, width: initial.width, height: initial.height }
    );
});

test('related work is connected without changing dependency depth', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'C'].map(id => node(id)),
        [edge(1, 'A', 'B', 'relates_to'), edge(2, 'B', 'C')]
    );

    assert.equal(placed(layout, 'A').depth, 0);
    assert.equal(placed(layout, 'B').depth, 0);
    assert.equal(placed(layout, 'C').depth, 1);
    assert.equal(layout.edges.find(item => item.key === 'work-edge-1').dashArray, '7 6');
});

test('work without an edge stays visible and is reported as disconnected', () => {
    const layout = buildWorkGraphLayout(
        ['A', 'B', 'E'].map(id => node(id)),
        [edge(1, 'A', 'B')]
    );

    assert.deepEqual(layout.disconnected.map(item => item.item.id), ['E']);
    assert.ok(placed(layout, 'E'));
    assert.equal(layout.nodes.length, 3);
});

test('strongly connected blocking edges share a column and are marked as a cycle', () => {
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

test('ten thousand nested work items use bounded hierarchy and queue traversal', () => {
    const size = 10_000;
    const nodes = Array.from({ length: size }, (_, index) => ({
        ...node(`N${String(index).padStart(5, '0')}`),
        ...(index ? { parent_id: `N${String(index - 1).padStart(5, '0')}` } : {})
    }));
    const metrics = {};
    const started = performance.now();
    const layout = buildWorkGraphLayout(nodes, [], { metrics });
    const elapsed = performance.now() - started;

    assert.equal(layout.nodes.length, size);
    assert.equal(layout.edges.filter(item => item.type === 'part_of').length, size - 1);
    assert.equal(placed(layout, 'N00000').depth, size - 1);
    assert.equal(placed(layout, 'N09999').depth, 0);
    assert.ok(metrics.hierarchySteps <= size, `hierarchy steps: ${metrics.hierarchySteps}`);
    assert.ok((metrics.queueComparisons || 0) < size * 40, `queue comparisons: ${metrics.queueComparisons || 0}`);
    assert.ok(elapsed < 5_000, `layout took ${Math.round(elapsed)}ms`);
});

test('large blocking graph keeps linear edge visits and preserves cycles, offsets, and paths', () => {
    const size = 1_000;
    const nodes = Array.from({ length: size }, (_, index) => node(`B${index}`));
    const edges = Array.from({ length: size - 1 }, (_, index) => edge(index, `B${index}`, `B${index + 1}`));
    edges.push(edge(size, 'B500', 'B499'));
    edges.push(edge(size + 1, 'B10', 'B900', 'relates_to'));
    const metrics = {};
    const layout = buildWorkGraphLayout(nodes, edges, {
        metrics,
        offsets: { 'node:B900': { x: 75, y: 40 } }
    });

    assert.equal(placed(layout, 'B900').offset.x, 75);
    assert.equal(placed(layout, 'B900').offset.y, 40);
    assert.equal(layout.cycles.length, 1);
    assert.deepEqual(layout.cycles[0].nodeIds, ['B499', 'B500']);
    assert.ok(layout.edges.every(item => typeof item.path === 'string' && item.path.startsWith('M ')));
    assert.ok((metrics.orderingIncomingVisits || 0) <= edges.length, `incoming visits: ${metrics.orderingIncomingVisits}`);
    assert.ok(metrics.topologyEdgeVisits <= edges.length * 3, `edge visits: ${metrics.topologyEdgeVisits}`);
});

test('every hierarchy item is an independent node joined child to parent', () => {
    const nodes = [
        node('P'),
        { ...node('C'), parent_id: 'P' },
        { ...node('G'), parent_id: 'C' }
    ];
    const layout = buildWorkGraphLayout(nodes, []);

    assert.deepEqual(layout.nodes.map(item => item.item.id).sort(), ['C', 'G', 'P']);
    assert.equal(placed(layout, 'G').depth, 0);
    assert.equal(placed(layout, 'C').depth, 1);
    assert.equal(placed(layout, 'P').depth, 2);
    assert.equal(placed(layout, 'C').parentItem.id, 'P');
    assert.deepEqual(placed(layout, 'C').childItems.map(item => item.id), ['G']);
    assert.equal(layout.edges.filter(item => item.type === 'part_of').length, 2);
    assert.ok(layout.edges.some(item => item.fromKey === 'G' && item.toKey === 'C'));
    assert.ok(layout.edges.some(item => item.fromKey === 'C' && item.toKey === 'P'));
    assert.equal(placed(layout, 'G').isEntry, false);
    assert.equal(placed(layout, 'C').isEntry, false);
    assert.equal(placed(layout, 'C').isOutcome, false);
    assert.equal(placed(layout, 'P').isOutcome, false);
});

test('hierarchy and dependency edges share one node-link layout', () => {
    const nodes = [node('P'), ...['A', 'B', 'C', 'D'].map(id => ({ ...node(id), parent_id: 'P' }))];
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')];
    const layout = buildWorkGraphLayout(nodes, edges);

    assert.equal(layout.nodes.length, 5);
    assert.equal(placed(layout, 'A').depth, 0);
    assert.equal(placed(layout, 'B').depth, 1);
    assert.equal(placed(layout, 'C').depth, 1);
    assert.equal(placed(layout, 'D').depth, 2);
    assert.equal(placed(layout, 'P').depth, 3);
    assert.equal(layout.edges.filter(item => item.type === 'blocks').length, 4);
    assert.equal(layout.edges.filter(item => item.type === 'part_of').length, 4);
});

test('moving a nested item keeps every incoming edge attached', () => {
    const nodes = [node('P'), ...['A', 'B', 'C', 'D'].map(id => ({ ...node(id), parent_id: 'P' }))];
    const edges = [edge(1, 'A', 'B'), edge(2, 'A', 'C'), edge(3, 'B', 'D'), edge(4, 'C', 'D')];
    const initial = buildWorkGraphLayout(nodes, edges);
    const moved = buildWorkGraphLayout(nodes, edges, { offsets: { 'node:D': { x: 90, y: 70 } } });

    assert.equal(placed(moved, 'D').x, placed(initial, 'D').x + 90);
    assert.equal(placed(moved, 'D').y, placed(initial, 'D').y + 70);
    const initialIncoming = initial.edges.filter(item => item.toKey === 'D').map(item => item.path);
    const movedIncoming = moved.edges.filter(item => item.toKey === 'D').map(item => item.path);
    assert.equal(movedIncoming.length, 2);
    assert.notDeepEqual(movedIncoming, initialIncoming);
});

test('an orphan parent keeps the work visible and reports the missing node', () => {
    const orphan = { ...node('X'), parent_id: 999 };
    const layout = buildWorkGraphLayout([orphan], []);

    assert.equal(layout.sourceNodeCount, 1);
    assert.equal(layout.nodes.length, 1);
    assert.equal(layout.disconnected.length, 1);
    assert.equal(placed(layout, 'X').orphanParentId, 999);
    assert.match(layout.hierarchyWarnings[0].label, /#999/);
});

test('a dependency across different parents keeps all four nodes and all relation types', () => {
    const nodes = [
        node('P'), node('Q'),
        { ...node('A'), parent_id: 'P' },
        { ...node('B'), parent_id: 'Q' }
    ];
    const layout = buildWorkGraphLayout(nodes, [edge(1, 'A', 'B')]);

    assert.equal(layout.nodes.length, 4);
    assert.ok(placed(layout, 'A'));
    assert.ok(placed(layout, 'B'));
    assert.equal(layout.edges.filter(item => item.type === 'part_of').length, 2);
    assert.equal(layout.edges.filter(item => item.type === 'blocks').length, 1);
    assert.ok(placed(layout, 'B').depth > placed(layout, 'A').depth);
    assert.equal(placed(layout, 'P').depth, 1);
    assert.equal(placed(layout, 'Q').depth, 2);
});

test('hierarchy links never create a blocking cycle', () => {
    const layout = buildWorkGraphLayout([
        node('A'), { ...node('B'), parent_id: 'A' }
    ], [edge(1, 'A', 'B')]);

    assert.equal(layout.cycles.length, 0);
    assert.equal(layout.edges.filter(item => item.cycle).length, 0);
    assert.equal(placed(layout, 'A').depth, placed(layout, 'B').depth);
});

test('hiding blocking links keeps dependency placement', () => {
    const nodes = ['A', 'B', 'C'].map(id => node(id));
    const edges = [edge(1, 'A', 'B'), edge(2, 'B', 'C')];
    const visible = buildWorkGraphLayout(nodes, edges);
    const hidden = buildWorkGraphLayout(nodes, edges, {
        relations: { part_of: true, blocks: false, relates_to: true }
    });

    assert.equal(hidden.edges.some(item => item.type === 'blocks'), false);
    for (const id of ['A', 'B', 'C']) {
        assert.deepEqual(
            { x: placed(hidden, id).x, y: placed(hidden, id).y, depth: placed(hidden, id).depth },
            { x: placed(visible, id).x, y: placed(visible, id).y, depth: placed(visible, id).depth }
        );
    }
});

test('focused work highlights every blocking predecessor and successor path', () => {
    const viewModel = workGraphViewModel();
    viewModel.setWorkGraph({
        nodes: ['A', 'B', 'C', 'D'].map(id => node(id)),
        edges: [edge(1, 'A', 'B'), edge(2, 'B', 'C'), edge(3, 'C', 'D')],
        worktrees: []
    });
    viewModel.focusGraphNode('C', false);

    const byKey = key => viewModel.workGraphLayout.edges.find(item => item.key === key);
    assert.equal(viewModel.graphEdgeClasses(byKey('work-edge-1'))['is-upstream'], true);
    assert.equal(viewModel.graphEdgeClasses(byKey('work-edge-2'))['is-upstream'], true);
    assert.equal(viewModel.graphEdgeClasses(byKey('work-edge-3'))['is-downstream'], true);
    for (const id of ['A', 'B', 'C', 'D']) {
        assert.equal(viewModel.graphNodeClasses(placed(viewModel.workGraphLayout, id))['is-path'], true);
    }
});

test('one SVG layer contains every generated relation path', () => {
    const viewModel = workGraphViewModel();
    viewModel.setWorkGraph({
        nodes: [node('P'), { ...node('C'), parent_id: 'P' }, node('N')],
        edges: [edge(1, 'N', 'C')],
        worktrees: []
    });

    const markup = viewModel.graphEdgesMarkup();
    assert.equal((markup.match(/<path\b/g) || []).length, viewModel.workGraphLayout.edges.length);
    assert.match(markup, /d="M [^"]+"/);
    assert.match(markup, /url\(#stash-work-graph-arrow-blocks\)/);
    assert.match(markup, /url\(#stash-work-graph-arrow-part-of\)/);
});

test('relation filters hide only selected links and keep parent-child navigation data', () => {
    const nodes = [
        node('P'), { ...node('C'), parent_id: 'P' }, node('A'), node('B')
    ];
    const layout = buildWorkGraphLayout(nodes, [
        edge(1, 'A', 'B', 'blocks'), edge(2, 'A', 'C', 'relates_to')
    ], { relations: { part_of: false, blocks: true, relates_to: false } });

    assert.deepEqual(layout.edges.map(item => item.type), ['blocks']);
    assert.equal(placed(layout, 'C').parentItem.id, 'P');
    assert.deepEqual(placed(layout, 'P').childItems.map(item => item.id), ['C']);
    assert.equal(placed(layout, 'B').depth, placed(layout, 'A').depth + 1);
});

test('focused work can move to every child and back to its parent while filters stay active', () => {
    const viewModel = workGraphViewModel();
    viewModel.setWorkGraph({
        nodes: [node('P'), { ...node('C1'), parent_id: 'P' }, { ...node('C2'), parent_id: 'P' }],
        edges: [], worktrees: []
    });

    viewModel.graphFilter.query = 'Task P';
    viewModel.refreshWorkGraphLayout();
    viewModel.focusGraphNode('P', false);
    assert.deepEqual(viewModel.graphFocusedChildren().map(item => item.id), ['C1', 'C2']);

    viewModel.focusGraphNodeByID('C2');
    assert.equal(viewModel.graphFocusedParent().id, 'P');
    assert.ok(placed(viewModel.workGraphLayout, 'C2'));
    assert.ok(placed(viewModel.workGraphLayout, 'P'));

    viewModel.toggleGraphRelation('part_of');
    assert.equal(viewModel.workGraphLayout.edges.some(item => item.type === 'part_of'), false);
    assert.equal(viewModel.graphFocusedParent().id, 'P');
});

test('closing the graph filter restores focus after the menu is hidden', () => {
    let nextTick = null;
    let focusOptions = null;
    const viewModel = {
        ...createWorkGraphViewModel(),
        $nextTick(callback) { nextTick = callback; }
    };
    const trigger = {
        isConnected: true,
        focus(options) { focusOptions = options; }
    };

    viewModel.toggleGraphFilterMenu(trigger);
    assert.equal(viewModel.graphFilterOpen, true);
    nextTick();
    nextTick = null;

    viewModel.closeGraphFilterMenu(true);
    assert.equal(viewModel.graphFilterOpen, false);
    assert.equal(focusOptions, null);
    assert.equal(typeof nextTick, 'function');

    nextTick();
    assert.deepEqual(focusOptions, { preventScroll: true });
});

test('the graph UI renders filter selection and direct parent-child navigation', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const graphArea = html.match(/<div x-show="view === 'graph'"[\s\S]*?<div x-show="view === 'worktrees'"/)?.[0] || '';

    assert.match(graphArea, /x-for="node in workGraphLayout\.nodes"/);
    assert.match(graphArea, /:class="graphNodeClasses\(node\)"/);
    assert.match(graphArea, /graphNodeMeta\(node\)/);
    assert.match(graphArea, /graphFilter\.query/);
    assert.match(graphArea, /graphFilter\.status/);
    assert.match(graphArea, /graphFilter\.agent/);
    assert.match(graphArea, /changeWorkGraphProject\(\)/);
    assert.match(graphArea, /class="stash-filter-trigger"/);
    assert.match(graphArea, /toggleGraphRelation\('part_of'\)/);
    assert.match(graphArea, /class="stash-filter-chips"/);
    assert.match(graphArea, /class="stash-graph-navigator"/);
    assert.match(graphArea, /focusGraphNodeByID\(graphFocusedParent\(\)\.id, true\)/);
    assert.match(graphArea, /x-for="child in graphFocusedChildren\(\)"/);
    assert.match(graphArea, /data-graph-node-key/);
    assert.match(graphArea, /class="stash-graph-node__actions"/);
    assert.match(graphArea, /class="stash-graph-canvas-tools"/);
    assert.match(graphArea, /<g x-html="graphEdgesMarkup\(\)"><\/g>/);
    assert.doesNotMatch(graphArea, /<template x-for="edge in workGraphLayout\.edges"[^>]*><path/);
    assert.match(graphArea, /graphViewportWheel/);
    assert.match(graphArea, /graphViewportKeydown/);
    assert.match(graphArea, /graphViewportFit/);
    assert.match(graphArea, /class="stash-graph-inspector"/);
    assert.match(graphArea, /class="stash-work-monitor"/);
    assert.match(graphArea, />받은 내용</);
    assert.match(graphArea, />다음 할 일</);
    assert.doesNotMatch(graphArea, /workGraphLayout\.stages|stash-graph-stage/);
    assert.doesNotMatch(graphArea, /childLayout|stash-graph-child|toggleGraphParent/);
});

test('drag handles stay in the graph view-model and never persist offsets', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const graphViewModel = fs.readFileSync(require.resolve('./ui/work-graph-view-model.js'), 'utf8');

    assert.match(graphViewModel, /graphNodeOffsets: \{\}/);
    assert.match(graphViewModel, /startGraphNodeDrag\(/);
    assert.match(graphViewModel, /moveGraphNodeWithKeyboard\(/);
    assert.match(graphViewModel, /resetGraphLayout\(/);
    assert.match(graphViewModel, /graphFocusedKey: ''/);
    assert.match(graphViewModel, /focusGraphNode\(/);
    assert.match(graphViewModel, /graphFocusedParent\(/);
    assert.match(graphViewModel, /graphFocusedChildren\(/);
    assert.match(graphViewModel, /scrollGraphNodeIntoView\(/);
    assert.match(graphViewModel, /return \{ minX: 0, minY: 0 \}/);
    assert.doesNotMatch(graphViewModel, /localStorage|sessionStorage/);
    assert.doesNotMatch(graphViewModel, /invokeTool\([^)]*graphNodeOffsets/);
    assert.match(html, /data-graph-drag-key/);
});

test('the graph shell fills its view and always has room for a real horizontal graph', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const graphCSS = fs.readFileSync(require.resolve('./ui/work-graph-board.css'), 'utf8');
    const hierarchy = buildWorkGraphLayout([
        node('P'), { ...node('C'), parent_id: 'P' }, { ...node('G'), parent_id: 'C' }
    ], []);

    assert.match(html, /\.stash-main \{[^}]*width: 100% !important;[^}]*max-width: none !important;/);
    assert.match(html, /@media \(min-width: 981px\) \{[\s\S]*?\.stash-main \{ padding-right: 0 !important; \}/);
    assert.match(html, /\.stash-graph \{[^}]*height: 100%;[^}]*min-height: 100%;[^}]*display: flex;[^}]*flex-direction: column;/);
    assert.match(html, /\.stash-graph-content \{[^}]*flex: 1 1 auto;[^}]*flex-direction: column;/);
    assert.match(html, /class="stash-graph-content"/);
    assert.match(graphCSS, /\.stash-root\.is-graph-view \.stash-main \{[^}]*height: calc\(100dvh - 46px\);/);
    assert.match(graphCSS, /\.stash-graph \{[\s\S]*?height: 100%;[\s\S]*?min-height: 0;/);
    assert.match(graphCSS, /\.stash-graph-content \{[^}]*flex: 1 1 auto;[^}]*flex-direction: column;/);
    assert.doesNotMatch(html, /<main class="[^"]*max-w-7xl/);
    assert.ok(hierarchy.width >= 760);
    assert.ok(hierarchy.height >= 360);
    assert.equal(Object.hasOwn(hierarchy, 'stages'), false);
    assert.equal(hierarchy.nodes.some(item => item.isEntry || item.isOutcome), false);
});
