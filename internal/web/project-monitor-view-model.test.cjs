const test = require('node:test');
const assert = require('node:assert/strict');

const { createProjectMonitorViewModel } = require('./ui/project-monitor-view-model.js');

const sampleMap = () => ({
    goal_tree: {
        root_goal_id: 10,
        goals: [{ id: 10, content: 'A를 출시한다', progress: 0.5 }]
    },
    work_items: [
        { id: 1, issue_key: 'W-1', title: '자료 연결', status: 'doing', priority: 2, agent_id: 'codex', attempt_status: 'active', next_action: '문서 범위를 확인한다' },
        { id: 2, issue_key: 'W-2', title: '답변 확인', status: 'blocked', priority: 3, owner: 'plato', latest_result: '샘플 10건 확인' },
        { id: 3, issue_key: 'W-3', title: '배포', status: 'done', priority: 1, agent_id: 'codex' }
    ],
    unassigned_work: [{ id: 4, issue_key: 'W-4', title: '목표 연결', status: 'ready', priority: 1 }],
    edges: [{ from: 'work:1', to: 'work:2', relation: 'blocks' }]
});

function monitor() {
    return {
        ...createProjectMonitorViewModel(),
        mapNamespaces: [{ slug: '/projects/demo', label: '데모 · /projects/demo' }],
        mapNamespaceSlug: '',
        routeInitialized: false,
        syncRoute() {},
        clearWorkMonitor() {},
        statusLabel(value) { return value; }
    };
}

test('project monitor summarizes and filters project work without dropping unassigned work', () => {
    const view = monitor();
    view.setProjectMonitorMap(sampleMap());

    assert.equal(view.projectMonitorRootGoal().content, 'A를 출시한다');
    assert.equal(view.projectMonitorProgress(view.projectMonitorRootGoal().progress), '50%');
    assert.deepEqual(view.projectMonitorAgents(), ['codex', 'plato']);
    assert.equal(view.projectMonitorCount('doing'), 1);
    assert.equal(view.projectMonitorCount('blocked'), 1);
    assert.deepEqual(view.projectMonitorRows().map(item => item.id), [2, 1, 4, 3]);
    assert.deepEqual(view.projectMonitorBlockers({ id: 2 }).map(item => item.id), [1]);

    view.projectMonitorFilter = { status: 'doing', agent: 'codex' };
    assert.deepEqual(view.projectMonitorRows().map(item => item.id), [1]);
});

test('project monitor fetches one project snapshot and only resumes the focused work', async () => {
    const view = monitor();
    const calls = [];
    view.loadMapNamespaces = async () => {};
    view.invokeTool = async (name, args) => {
        calls.push({ name, args });
        if (name === 'get_goal_map') return sampleMap();
        throw new Error('unexpected tool: ' + name);
    };
    view.toolValue = value => value;
    view.markLoaded = () => {};
    view.setNotice = () => {};
    view.loadWorkMonitor = async id => calls.push({ name: 'resume_work', args: { work_item_id: id, detail: 'brief' } });

    await view.loadProjectMonitor(false);
    assert.equal(view.projectMonitorProjectSlug, '/projects/demo');
    assert.deepEqual(calls, [{ name: 'get_goal_map', args: { namespace: '/projects/demo', include_done: true } }]);

    await view.selectProjectMonitorWork({ id: 2 });
    assert.equal(view.projectMonitorSelectedID, 2);
    assert.deepEqual(calls.at(-1), { name: 'resume_work', args: { work_item_id: 2, detail: 'brief' } });
});

test('completed blockers no longer count as blocking work', () => {
    const view = monitor();
    const value = sampleMap();
    value.work_items[0].status = 'done';
    view.setProjectMonitorMap(value);

    assert.deepEqual(view.projectMonitorBlockers({ id: 2 }), []);
});

test('a focused brief immediately reconciles an expired row with its current status', async () => {
    const view = monitor();
    view.setProjectMonitorMap(sampleMap());
    view.loadWorkMonitor = async () => {
        view.workMonitorBrief = {
            work_item: { id: 1, status: 'ready' },
            latest_attempt: { agent_id: 'codex', status: 'expired', lease_expires_at: '2026-08-30T00:00:00Z' },
            latest_checkpoint: { result: '문서 8개 연결', next_action: '나머지 문서를 확인한다' },
            next_action: '나머지 문서를 확인한다'
        };
    };

    await view.selectProjectMonitorWork({ id: 1 });
    const item = view.projectMonitorSelectedItem();
    assert.equal(item.status, 'ready');
    assert.equal(item.attempt_status, 'expired');
    assert.equal(item.latest_result, '문서 8개 연결');
    assert.equal(item.next_action, '나머지 문서를 확인한다');
});
