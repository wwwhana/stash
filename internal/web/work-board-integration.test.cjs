const test = require('node:test');
const assert = require('node:assert/strict');

const { createConsoleViewModel } = require('./ui/console-app.js');

test('board listing sends the selected project namespace set', async () => {
    const vm = createConsoleViewModel();
    vm.mapNamespaces = [
        { slug: '/projects/demo' },
        { slug: '/projects/demo/api' },
        { slug: '/projects/other' }
    ];
    vm.boardProjectSlug = '/projects/demo';
    let request = null;
    vm.invokeTool = async (tool, args) => {
        request = { tool, args };
        return [];
    };
    vm.toolValue = value => value;
    vm.pageSlice = () => ({ items: [], hasMore: false, nextOffset: 0 });
    vm.syncRoute = () => {};
    vm.markLoaded = () => {};
    vm.setNotice = () => {};

    await vm.loadWorkView('board');

    assert.equal(request.tool, 'list_work_items');
    assert.equal(request.args.namespaces, '/projects/demo');
});

test('new issue is stored in the selected board scope', async () => {
    const vm = createConsoleViewModel();
    vm.boardProjectSlug = '/projects/demo';
    vm.boardNamespaceSlug = '/projects/demo/api';
    vm.issueForm = { title: 'API 확인', description: '', issueType: 'task', labels: '' };
    let request = null;
    vm.invokeTool = async (tool, args) => { request = { tool, args }; };
    vm.loadWorkBoard = async () => {};
    vm.setNotice = () => {};

    await vm.createIssue();

    assert.equal(request.tool, 'create_work_item');
    assert.equal(request.args.namespace, '/projects/demo/api');
});

test('an older board response cannot overwrite a newer filter result', async () => {
    const vm = createConsoleViewModel();
    const pending = [];
    vm.clearWorkGraph = () => { vm.graph = { nodes: [], edges: [], worktrees: [] }; };
    vm.setWorkGraph = value => { vm.graph = value; };
    vm.invokeTool = async () => new Promise(resolve => pending.push(resolve));
    vm.toolValue = value => value;
    vm.pageSlice = value => ({ items: value, hasMore: false, nextOffset: 0 });
    vm.syncRoute = () => {};
    vm.markLoaded = () => {};
    vm.setNotice = () => {};

    vm.boardFilter.q = '이전';
    const older = vm.loadWorkView('board');
    vm.boardFilter.q = '최신';
    const newer = vm.loadWorkView('board');
    pending[1]([{ id: 2, title: '최신' }]);
    await newer;
    pending[0]([{ id: 1, title: '이전' }]);
    await older;

    assert.deepEqual(vm.graph.nodes.map(item => item.id), [2]);
});

test('keyboard status control updates the card in place and keeps position ordered', async () => {
    const vm = createConsoleViewModel();
    vm.graph = {
        nodes: [
            { id: 1, title: '대상', status: 'ready', position: 2 },
            { id: 2, title: '기존 진행', status: 'doing', position: 4 },
            { id: 3, title: '또 다른 진행', status: 'doing', position: 9 }
        ], edges: [], worktrees: []
    };
    vm.invokeTool = async (tool, args) => {
        assert.equal(tool, 'update_work_item');
        assert.deepEqual(args, { id: 1, status: 'doing', position: 10 });
    };
    vm.setNotice = () => {};
    vm.syncRoute = () => {};
    const control = { value: 'doing' };

    await vm.changeBoardItemStatus(vm.graph.nodes[0], 'doing', control);

    assert.equal(vm.graph.nodes.find(item => item.id === 1).status, 'doing');
    assert.equal(vm.graph.nodes.find(item => item.id === 1).position, 10);
});

test('keyboard status control restores its value when a save is already running', async () => {
    const vm = createConsoleViewModel();
    vm.loading = true;
    const control = { value: 'done' };
    await vm.changeBoardItemStatus({ id: 1, status: 'ready' }, 'done', control);
    assert.equal(control.value, 'ready');
});
