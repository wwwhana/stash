const test = require('node:test');
const assert = require('node:assert/strict');

const { createWorkGraphViewModel } = require('./ui/work-graph-view-model.js');

function node(id) {
    return { id, issue_key: `W-${id}`, title: `작업 ${id}`, status: 'ready', priority: 0, position: 0 };
}

function edge(id, from, to) {
    return { id, from_item_id: from, to_item_id: to, edge_type: 'blocks' };
}

function viewModel() {
    return {
        ...createWorkGraphViewModel(),
        view: 'graph',
        loading: false,
        mapNamespaceSlug: '/projects/demo',
        graphProjectSlug: '/projects/demo',
        syncRoute() {},
        statusLabel(value) { return value; },
        markLoaded() { this.loaded = true; },
        loadWorkMonitor() {},
        $nextTick() {}
    };
}

test('the next graph page appends independent nodes and edges', async () => {
    const vm = viewModel();
    vm.setWorkGraph({
        nodes: [node(1)], edges: [edge(1, 1, 2)], worktrees: [],
        has_more: true, next_node_offset: 1, next_edge_offset: 1, total_nodes: 2, total_edges: 2
    });
    let request = null;
    vm.invokeTool = async (tool, args) => {
        request = { tool, args };
        return {
            nodes: [node(2)], edges: [edge(2, 2, 1)], worktrees: [],
            has_more: false, next_node_offset: 2, next_edge_offset: 2, total_nodes: 2, total_edges: 2
        };
    };
    vm.toolValue = value => value;

    await vm.loadMoreWorkGraph();

    assert.deepEqual(vm.graph.nodes.map(item => item.id), [1, 2]);
    assert.deepEqual(vm.graph.edges.map(item => item.id), [1, 2]);
    assert.equal(vm.graphPage.hasMore, false);
    assert.deepEqual(request, {
        tool: 'get_work_graph',
        args: { include_done: true, node_offset: 1, edge_offset: 1, project: '/projects/demo' }
    });
});

test('focusing a later page loads the target and its complete hierarchy', async () => {
    const vm = viewModel();
    vm.setWorkGraph({
        nodes: [node(1)], edges: [], worktrees: [],
        has_more: true, next_node_offset: 1, next_edge_offset: 0, total_nodes: 3, total_edges: 0
    });
    const pages = [
        { nodes: [{ ...node(2), parent_id: 1 }], edges: [], worktrees: [], has_more: true, next_node_offset: 2, next_edge_offset: 0, total_nodes: 3, total_edges: 0 },
        { nodes: [{ ...node(3), parent_id: 1 }], edges: [], worktrees: [], has_more: false, next_node_offset: 3, next_edge_offset: 0, total_nodes: 3, total_edges: 0 }
    ];
    vm.invokeTool = async () => pages.shift();
    vm.toolValue = value => value;

    assert.equal(await vm.focusGraphNodeByID(3), true);
    assert.equal(vm.graphFocusedKey, '3');
    assert.equal(vm.graphFocusedParent().id, 1);
    assert.deepEqual(vm.graphChildrenFor(node(1)).map(item => item.id), [2, 3]);
    assert.equal(vm.graphPage.hasMore, false);
});

test('selecting a loaded parent finishes node pages before claiming there are no children', async () => {
    const vm = viewModel();
    vm.setWorkGraph({
        nodes: [node(1)], edges: [], worktrees: [],
        has_more: true, next_node_offset: 1, next_edge_offset: 0, total_nodes: 2, total_edges: 0
    });
    vm.invokeTool = async () => ({
        nodes: [{ ...node(2), parent_id: 1 }], edges: [], worktrees: [],
        has_more: false, next_node_offset: 2, next_edge_offset: 0, total_nodes: 2, total_edges: 0
    });
    vm.toolValue = value => value;

    assert.equal(await vm.focusGraphNodeByID(1), true);
    assert.deepEqual(vm.graphFocusedChildren().map(item => item.id), [2]);
    assert.equal(vm.graphFocusError, '');
});

test('a page from an old graph scope is discarded', async () => {
    const vm = viewModel();
    vm.setWorkGraph({
        nodes: [node(1)], edges: [], worktrees: [],
        has_more: true, next_node_offset: 1, next_edge_offset: 0, total_nodes: 2, total_edges: 0
    });
    let resolvePage;
    vm.invokeTool = async () => new Promise(resolve => { resolvePage = resolve; });
    vm.toolValue = value => value;

    const pending = vm.loadMoreWorkGraph();
    vm.clearWorkGraph();
    resolvePage({ nodes: [node(2)], edges: [], worktrees: [], has_more: false });
    await pending;

    assert.deepEqual(vm.graph.nodes, []);
    assert.equal(vm.graphPageLoading, false);
});

test('search can explicitly load the remaining graph pages before filtering', async () => {
    const vm = viewModel();
    vm.graphFilter.query = '문서';
    vm.setWorkGraph({
        nodes: [{ ...node(1), title: '문서 시작' }], edges: [], worktrees: [],
        has_more: true, next_node_offset: 1, next_edge_offset: 0, total_nodes: 2, total_edges: 0
    });
    vm.invokeTool = async () => ({
        nodes: [{ ...node(2), title: '문서 끝' }], edges: [], worktrees: [],
        has_more: false, next_node_offset: 2, next_edge_offset: 0, total_nodes: 2, total_edges: 0
    });
    vm.toolValue = value => value;

    assert.equal(await vm.loadAllWorkGraphForSearch(), true);
    assert.deepEqual(vm.graph.nodes.map(item => item.id), [1, 2]);
    assert.deepEqual(vm.workGraphLayout.nodes.map(item => item.item.id), [1, 2]);
});
