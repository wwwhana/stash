const test = require('node:test');
const assert = require('node:assert/strict');

const {
    MIN_SCALE,
    MAX_SCALE,
    createGraphViewportViewModel
} = require('./ui/graph-viewport-view-model.js');

function viewport(width = 500, height = 320) {
    return {
        clientWidth: width,
        clientHeight: height,
        getBoundingClientRect() {
            return { left: 100, top: 40, width, height };
        }
    };
}

function event(overrides = {}) {
    return {
        isPrimary: true,
        button: 0,
        pointerId: 1,
        clientX: 140,
        clientY: 100,
        currentTarget: viewport(),
        target: { closest() { return null; } },
        cancelable: true,
        preventDefault() { this.prevented = true; },
        ...overrides
    };
}

test('zoom is bounded and both directions change the scale', () => {
    const vm = { ...createGraphViewportViewModel() };

    vm.graphViewport.scale = MIN_SCALE;
    vm.zoomGraphOut();
    assert.equal(vm.graphViewport.scale, MIN_SCALE);

    vm.graphViewport.scale = MAX_SCALE;
    vm.zoomGraphIn();
    assert.equal(vm.graphViewport.scale, MAX_SCALE);

    vm.graphViewport.scale = 1;
    vm.zoomGraphIn();
    assert.ok(vm.graphViewport.scale > 1 && vm.graphViewport.scale <= MAX_SCALE);
    vm.graphViewport.scale = 1;
    vm.zoomGraphOut();
    assert.ok(vm.graphViewport.scale < 1);
});

test('Alt and wheel zoom keeps the world point under the pointer', () => {
    const vm = { ...createGraphViewportViewModel() };
    const target = viewport();
    vm.graphViewport = { ...vm.graphViewport, panX: 20, panY: 30, scale: 1 };
    const raw = event({
        currentTarget: target,
        clientX: 230,
        clientY: 160,
        deltaY: -1,
        altKey: true
    });
    const before = vm.graphViewportInversePoint({ x: 130, y: 120 });
    vm.graphViewportWheel(raw);
    const after = vm.graphViewportInversePoint({ x: 130, y: 120 });

    assert.equal(raw.prevented, true);
    assert.ok(Math.abs(after.x - before.x) < 0.000001);
    assert.ok(Math.abs(after.y - before.y) < 0.000001);
});

test('plain wheel pans without changing the scale', () => {
    const vm = { ...createGraphViewportViewModel() };
    const raw = event({ deltaX: 12, deltaY: 30 });
    vm.graphViewport = { ...vm.graphViewport, panX: 20, panY: 10, scale: 1.2 };

    vm.graphViewportWheel(raw);

    assert.equal(raw.prevented, true);
    assert.deepEqual(
        { x: vm.graphViewport.panX, y: vm.graphViewport.panY, scale: vm.graphViewport.scale },
        { x: 8, y: -20, scale: 1.2 }
    );
    assert.equal(vm.graphViewportAnnouncement, '작업 흐름 위치를 옮겼습니다.');
});

test('browser zoom wheel is not intercepted', () => {
    const vm = { ...createGraphViewportViewModel() };
    const raw = event({ deltaY: -20, ctrlKey: true });
    const before = { ...vm.graphViewport };

    vm.graphViewportWheel(raw);

    assert.equal(raw.prevented, undefined);
    assert.deepEqual(vm.graphViewport, before);
});

test('a zero-delta wheel event does not change the viewport', () => {
    const vm = { ...createGraphViewportViewModel() };
    vm.graphViewport = { ...vm.graphViewport, panX: 12, panY: -7, scale: 1.2 };
    const before = { ...vm.graphViewport };
    vm.graphViewportWheel(event({ deltaY: 0 }));
    assert.deepEqual(vm.graphViewport, before);
});

test('fit chooses a bounded scale and centers the graph in the viewport', () => {
    const vm = { ...createGraphViewportViewModel() };
    const target = viewport(500, 320);
    vm.setGraphViewportLayout({ width: 1000, height: 500 });
    vm.fitGraphViewport(target);

    assert.ok(vm.graphViewport.scale >= MIN_SCALE && vm.graphViewport.scale <= MAX_SCALE);
    const center = vm.graphViewportTransformPoint({ x: 500, y: 250 });
    assert.ok(Math.abs(center.x - 250) < 0.000001);
    assert.ok(Math.abs(center.y - 160) < 0.000001);
});

test('window resizing keeps the same graph center in view', () => {
    const vm = { ...createGraphViewportViewModel() };
    vm.setGraphViewportLayout({ width: 800, height: 400 }, viewport(500, 320));
    vm.fitGraphViewport(viewport(500, 320));
    const before = vm.graphViewportTransformPoint({ x: 400, y: 200 });

    vm.resizeGraphViewport(viewport(900, 520));

    const after = vm.graphViewportTransformPoint({ x: 400, y: 200 });
    assert.deepEqual(before, { x: 250, y: 160 });
    assert.deepEqual(after, { x: 450, y: 260 });
});

test('fit can shrink below the manual zoom limit to show a large graph', () => {
    const vm = { ...createGraphViewportViewModel() };
    const target = viewport(1200, 700);
    vm.setGraphViewportLayout({ width: 37232, height: 360 });
    vm.fitGraphViewport(target);

    assert.ok(vm.graphViewport.scale < MIN_SCALE);
    assert.ok(37232 * vm.graphViewport.scale <= 1200);
    vm.zoomGraphOut();
    assert.equal(vm.graphViewport.scale, MIN_SCALE);
});

test('fit waits for a visible viewport instead of moving the graph off-screen', () => {
    const vm = { ...createGraphViewportViewModel() };
    vm.graphViewport = { ...vm.graphViewport, panX: 12, panY: 18 };
    vm.setGraphViewportLayout({ width: 1000, height: 500 });
    vm.fitGraphViewport({ width: 0, height: 0 });
    assert.deepEqual(
        { x: vm.graphViewport.panX, y: vm.graphViewport.panY, scale: vm.graphViewport.scale },
        { x: 12, y: 18, scale: 1 }
    );
    assert.equal(vm.scheduleGraphViewportFit(viewport(500, 320), { width: 1000, height: 500 }), true);
    assert.equal(vm.graphViewportTransformPoint({ x: 500, y: 250 }).x, 250);
    assert.equal(vm.graphViewportTransformPoint({ x: 500, y: 250 }).y, 160);
});

test('empty-canvas drag moves the pan and node targets do not start it', () => {
    const vm = { ...createGraphViewportViewModel() };
    const start = event({ clientX: 120, clientY: 80 });
    assert.equal(vm.startGraphViewportPan(start), true);
    assert.equal(vm.moveGraphViewportPan(event({ clientX: 175, clientY: 115 })), true);
    assert.deepEqual(
        { x: vm.graphViewport.panX, y: vm.graphViewport.panY },
        { x: 55, y: 35 }
    );
    assert.equal(vm.finishGraphViewportPan(event({ pointerId: 1 })), true);
    assert.equal(vm.graphViewportDragging, false);

    const nodeTarget = event({ target: { closest(selector) { return selector.includes('data-graph-node-key') ? {} : null; } } });
    assert.equal(vm.startGraphViewportPan(nodeTarget), false);
});

test('node centering and world/space styles use the same transform state', () => {
    const vm = { ...createGraphViewportViewModel() };
    const target = viewport(400, 300);
    vm.setGraphViewportLayout({ width: 800, height: 400 }, target);
    vm.centerGraphNode({ x: 600, y: 100 }, target);

    assert.deepEqual(
        { x: vm.graphViewport.panX, y: vm.graphViewport.panY },
        { x: -400, y: 50 }
    );
    assert.match(vm.graphViewportWorldStyle(), /transform:translate\(-400px,50px\) scale\(1\)/);
    assert.match(vm.graphViewportSpaceStyle(target), /width:800px/);
    assert.match(vm.graphViewportSpaceStyle(target), /height:450px/);
});

test('viewport and node helpers accept plain geometry records', () => {
    const vm = { ...createGraphViewportViewModel() };
    vm.setGraphViewportLayout({ width: 800, height: 400 });
    vm.fitGraphViewport({ width: 400, height: 300 });
    vm.workGraphLayout = { nodes: [{ key: 'node-a', item: { id: 7 }, x: 400, y: 200 }] };
    vm.centerGraphNodeByID(7, { width: 400, height: 300 });
    assert.equal(vm.graphViewport.panX, 24);
    assert.equal(vm.graphViewport.panY, 62);
});

test('keyboard controls pan, zoom, fit, and center the selected work', () => {
    const vm = { ...createGraphViewportViewModel() };
    const target = viewport(500, 320);
    vm.setGraphViewportLayout({ width: 1000, height: 500 }, target);
    const key = value => event({ key: value, currentTarget: target });

    const right = key('ArrowRight');
    assert.equal(vm.graphViewportKeydown(right, target), true);
    assert.equal(vm.graphViewport.panX, -48);
    assert.equal(right.prevented, true);

    const beforeZoom = vm.graphViewport.scale;
    vm.graphViewportKeydown(key('+'), target);
    assert.ok(vm.graphViewport.scale > beforeZoom);
    assert.match(vm.graphViewportAnnouncement, /^확대 /);

    vm.graphViewportKeydown(key('Home'), target, { width: 1000, height: 500 });
    assert.match(vm.graphViewportAnnouncement, /^전체 보기 /);

    vm.graphFocusedKey = '7';
    vm.workGraphLayout = { nodes: [{ key: '7', item: { id: 7 }, x: 800, y: 200 }] };
    vm.graphViewportKeydown(key('End'), target);
    assert.equal(vm.graphViewportTransformPoint({ x: 800, y: 200 }).x, 250);
    assert.equal(vm.graphViewportTransformPoint({ x: 800, y: 200 }).y, 160);
});
