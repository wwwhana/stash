const test = require('node:test');
const assert = require('node:assert/strict');

const { createWorkBoardScopeViewModel } = require('./ui/work-board-scope-view-model.js');

function viewModel() {
    return {
        ...createWorkBoardScopeViewModel(),
        mapNamespacesLoaded: true,
        mapNamespaces: [
            { slug: '/projects/demo', label: 'Demo' },
            { slug: '/projects/demo/api', label: 'API' },
            { slug: '/projects/other', label: 'Other' },
            { slug: '/self', label: 'Self' }
        ],
        loadWorkBoard() { this.loaded = true; }
    };
}

test('project scope includes the project and only its descendants', () => {
    const vm = viewModel();
    vm.boardProjectSlug = '/projects/demo';
    assert.equal(vm.workBoardNamespacesArgument(), '/projects/demo');
    assert.deepEqual(vm.boardNamespaces().map(item => item.slug), ['/projects/demo/api']);
});

test('narrow scope overrides project scope and sets the creation location', () => {
    const vm = viewModel();
    vm.boardProjectSlug = '/projects/demo';
    vm.boardNamespaceSlug = '/projects/demo/api';
    assert.equal(vm.workBoardNamespacesArgument(), '/projects/demo/api');
    assert.equal(vm.workBoardCreationNamespace(), '/projects/demo/api');
});

test('selecting a namespace restores its owning project', async () => {
    const vm = viewModel();
    vm.boardNamespaceSlug = '/projects/demo/api';
    await vm.changeWorkBoardNamespace();
    assert.equal(vm.boardProjectSlug, '/projects/demo');
    assert.equal(vm.loaded, true);
});

test('all scope includes every namespace the user can see', () => {
    const vm = viewModel();
    assert.equal(vm.workBoardNamespacesArgument(), '/');
    assert.equal(vm.workBoardCreationNamespace(), '/');
});
