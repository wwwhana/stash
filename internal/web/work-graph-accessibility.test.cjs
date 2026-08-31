const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');

const { createWorkGraphViewModel } = require('./ui/work-graph-view-model.js');

function graphViewModel() {
    return {
        ...createWorkGraphViewModel(),
        $nextTick(callback) { callback(); },
        syncRoute() {},
        refreshWorkGraphLayout() {},
        clearWorkMonitor() {},
        loadWorkMonitor() {},
        scrollGraphNodeIntoView() {},
        statusLabel(value) { return value; }
    };
}

test('small-screen inspector traps Tab at both ends', () => {
    const previousWindow = global.window;
    const previousDocument = global.document;
    const first = { hidden: false, getAttribute() { return null; }, focus() { global.document.activeElement = this; } };
    const last = { hidden: false, getAttribute() { return null; }, focus() { global.document.activeElement = this; } };
    const inspector = {
        matches() { return true; },
        querySelectorAll() { return [first, last]; },
        contains(element) { return element === first || element === last; }
    };
    global.window = {
        matchMedia() { return { matches: true }; },
        getComputedStyle() { return { display: 'block', visibility: 'visible' }; }
    };
    global.document = { activeElement: last, querySelector() { return inspector; } };
    const vm = graphViewModel();
    const event = {
        key: 'Tab', currentTarget: inspector, cancelable: true,
        preventDefault() { this.prevented = true; }
    };

    assert.equal(vm.trapGraphInspectorFocus(event), true);
    assert.equal(event.prevented, true);
    assert.equal(global.document.activeElement, first);

    global.document.activeElement = first;
    assert.equal(vm.trapGraphInspectorFocus({ ...event, shiftKey: true, prevented: false }), true);
    assert.equal(global.document.activeElement, last);
    global.window = previousWindow;
    global.document = previousDocument;
});

test('Escape closes one graph layer and restores the selected node focus', () => {
    const vm = graphViewModel();
    const returnElement = { isConnected: true, focused: false, focus() { this.focused = true; } };
    vm.graph.nodes = [{ id: 7, title: '선택 작업', status: 'ready' }];
    vm.focusGraphNode('7', false, returnElement, false);
    const event = {
        cancelable: true,
        preventDefault() { this.prevented = true; },
        stopPropagation() { this.stopped = true; }
    };

    assert.equal(vm.handleGraphEscape(event), true);
    assert.equal(vm.graphFocusedKey, '');
    assert.equal(returnElement.focused, true);
    assert.equal(event.prevented, true);
    assert.equal(event.stopped, true);
});

test('Escape cancels an active move before closing the inspector', () => {
    const vm = graphViewModel();
    vm.graphFocusedKey = '7';
    vm.graphDragState = { key: 'node:7' };
    vm.cancelGraphNodeDrag = () => { vm.graphDragState = null; };

    assert.equal(vm.handleGraphEscape({ cancelable: false, stopPropagation() {} }), true);
    assert.equal(vm.graphDragState, null);
    assert.equal(vm.graphFocusedKey, '7');
});

test('filter disclosure returns focus when it closes without a new focus target', () => {
    const previousDocument = global.document;
    let activeElement = null;
    let triggerFocusOptions = null;
    const search = {
        focus() { activeElement = this; }
    };
    const menu = {
        contains(element) { return element === search; }
    };
    const trigger = {
        isConnected: true,
        focus(options) {
            triggerFocusOptions = options;
            activeElement = this;
        }
    };
    global.document = {
        get activeElement() { return activeElement; },
        getElementById(id) {
            return id === 'work-graph-filter-query' ? search : menu;
        }
    };

    try {
        const vm = graphViewModel();
        vm.toggleGraphFilterMenu(trigger);
        assert.equal(vm.graphFilterOpen, true);
        assert.equal(activeElement, search);

        vm.closeGraphFilterMenu(false, true);
        assert.equal(vm.graphFilterOpen, false);
        assert.deepEqual(triggerFocusOptions, { preventScroll: true });
        assert.equal(activeElement, trigger);
    } finally {
        global.document = previousDocument;
    }
});

test('closing an inspector restores the selected node when no opener was recorded', () => {
    const previousDocument = global.document;
    let focused = false;
    const nodeButton = {
        dataset: { graphNodeOpen: '7' },
        isConnected: true,
        focus() { focused = true; }
    };
    global.document = {
        querySelectorAll(selector) {
            assert.equal(selector, '[data-graph-node-open]');
            return [nodeButton];
        }
    };

    try {
        const vm = graphViewModel();
        vm.graph.nodes = [{ id: 7, title: '선택 작업', status: 'ready' }];
        vm.graphFocusedKey = '7';
        vm.clearGraphFocus(true);
        assert.equal(focused, true);
    } finally {
        global.document = previousDocument;
    }
});

test('graph popups expose dialog semantics and mobile controls have touch-sized targets', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const css = fs.readFileSync(require.resolve('./ui/work-graph-board.css'), 'utf8');
    assert.match(html, /class="stash-filter-trigger"[\s\S]*aria-haspopup="dialog"/);
    assert.match(html, /id="work-graph-filter-menu"[\s\S]*role="dialog"/);
    assert.match(html, /@click\.outside="closeGraphFilterMenu\(false, true\)"/);
    assert.match(html, /class="stash-graph-inspector"[\s\S]*:role="graphInspectorIsOverlay\(\) \? 'dialog' : 'complementary'"/);
    assert.match(html, /class="stash-graph-inspector"[\s\S]*:aria-modal="graphInspectorIsOverlay\(\) \? 'true' : null"/);
    assert.match(html, /:data-graph-node-open="node\.key"/);
    assert.match(css, /\.stash-graph \.stash-filter-menu button, \.stash-graph \.stash-filter-chip \{ min-height: 44px; \}/);
});
