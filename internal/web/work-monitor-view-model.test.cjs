const test = require('node:test');
const assert = require('node:assert/strict');
const { createWorkMonitorViewModel } = require('./ui/work-monitor-view-model.js');

function viewModel(extra = {}) {
    return {
        ...createWorkMonitorViewModel(),
        mapNamespaces: [],
        mapNamespaceSlug: '',
        graphFocusedKey: '11',
        graph: { nodes: [{ id: 11, title: '답변 근거 확인', status: 'doing', owner: 'codex' }] },
        graphFocusedItem() { return this.graph.nodes.find(item => String(item.id) === String(this.graphFocusedKey)) || null; },
        ...extra
    };
}

test('project and namespace selectors keep their scopes separate', async () => {
    let loads = 0;
    const monitor = viewModel({
        mapNamespaces: [
            { slug: '/projects/atlas', label: 'Atlas' },
            { slug: '/projects/atlas/ingest', label: '수집' },
            { slug: '/projects/other', label: 'Other' },
            { slug: '/self', label: 'Self' }
        ],
        mapNamespaceSlug: '/projects/atlas/ingest',
        async loadWorkGraph() { loads += 1; }
    });

    monitor.syncGraphProjectFromNamespace();
    assert.equal(monitor.graphProjectSlug, '/projects/atlas');
    assert.deepEqual(monitor.graphNamespaces().map(item => item.slug), ['/projects/atlas/ingest']);

    monitor.mapNamespaceSlug = '/projects/atlas';
    monitor.syncGraphProjectFromNamespace();
    assert.equal(monitor.mapNamespaceSlug, '');

    monitor.graphProjectSlug = '/projects/other';
    monitor.mapNamespaceSlug = '/projects/other';
    await monitor.changeWorkGraphProject();
    assert.equal(monitor.mapNamespaceSlug, '');
    assert.equal(monitor.graphFocusedKey, '');
    assert.equal(loads, 1);
});

test('selected work monitor joins goal, agent, blocker, result, next action, evidence, and exact input size', () => {
    const monitor = viewModel();
    monitor.workMonitorBrief = {
        shared_goal: { id: 1, content: 'A: 고객 지원 지식 서비스를 출시한다' },
        goal_context: { path: [{ id: 1, content: 'A' }, { id: 2, content: 'A-1' }, { id: 3, content: 'A-1-1' }] },
        work_item: { id: 11, title: 'Confluence 연결', status: 'blocked', owner: 'fallback' },
        latest_attempt: { agent_id: 'agent-a' },
        latest_checkpoint: { result: '20개 문서를 확인했다.', next_action: '남은 문서의 권한을 요청한다.' },
        blockers: [{ title: '접근 권한 대기' }],
        evidence_references: [
            { summary: '문서 목록', reference: 'confluence://atlas/docs' },
            { summary: '권한 확인 화면', reference: 'screen://permissions' }
        ],
        context_window: { input_bytes: 1536, input_limit_bytes: 4096, truncated: true }
    };

    const summary = monitor.selectedWorkMonitor();
    assert.equal(summary.agent, 'agent-a');
    assert.equal(summary.blocker, '접근 권한 대기');
    assert.equal(summary.result, '20개 문서를 확인했다.');
    assert.equal(summary.nextAction, '남은 문서의 권한을 요청한다.');
    assert.equal(summary.blockedByStatus, true);
    assert.deepEqual(summary.blockers, [{ text: '접근 권한 대기', reference: '' }]);
    assert.equal(summary.evidenceReference, 'confluence://atlas/docs');
    assert.deepEqual(summary.evidenceItems, [
        { text: '문서 목록', reference: 'confluence://atlas/docs' },
        { text: '권한 확인 화면', reference: 'screen://permissions' }
    ]);
    assert.deepEqual(summary.goalPath.map(goal => goal.content), ['A', 'A-1', 'A-1-1']);
    assert.equal(monitor.monitorInputLabel(summary), '1.5 KB / 4.0 KB · 일부 표시');
});

test('loading one selected item uses a bounded brief', async () => {
    const pending = [];
    const monitor = viewModel({
        invokeTool(name, args) {
            pending.push({ name, args });
            return Promise.resolve({ value: { work_item: { id: args.work_item_id, status: 'ready' } } });
        },
        toolValue(response) { return response.value; }
    });

    await monitor.loadWorkMonitor(11);
    assert.deepEqual(pending, [{ name: 'resume_work', args: { work_item_id: 11, detail: 'brief' } }]);
    assert.equal(monitor.workMonitorBrief.work_item.id, 11);
    assert.equal(monitor.workMonitorLoading, false);
    assert.match(monitor.monitorInputLabel(), /^약 \d+ B$/);
});

test('an expired active lease is visible as expired in the selected summary', () => {
    const monitor = viewModel({
        workMonitorBrief: {
            work_item: { id: 11, status: 'doing' },
            latest_attempt: { status: 'active', lease_expires_at: '2000-01-01T00:00:00Z' }
        }
    });

    assert.equal(monitor.selectedWorkMonitorStatus(), 'expired');
});

test('a failed selected-work request can be retried and replaced with current data', async () => {
    let calls = 0;
    const monitor = viewModel({
        async invokeTool(name, args) {
            calls += 1;
            if (calls === 1) throw new Error('잠시 연결할 수 없습니다.');
            return { value: { work_item: { id: args.work_item_id, status: 'ready' } } };
        },
        toolValue(response) { return response.value; }
    });

    await monitor.loadWorkMonitor(11);
    assert.equal(monitor.workMonitorError, '잠시 연결할 수 없습니다.');

    await monitor.loadWorkMonitor(11, true);
    assert.equal(calls, 2);
    assert.equal(monitor.workMonitorError, '');
    assert.equal(monitor.workMonitorBrief.work_item.status, 'ready');
});
