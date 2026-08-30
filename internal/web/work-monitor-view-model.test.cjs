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

test('selected work monitor joins goal, agent, blocker, result, evidence, and exact input size', () => {
    const monitor = viewModel();
    monitor.workMonitorBrief = {
        shared_goal: { id: 1, content: 'A: 고객 지원 지식 서비스를 출시한다' },
        goal_context: { path: [{ id: 1, content: 'A' }, { id: 2, content: 'A-1' }, { id: 3, content: 'A-1-1' }] },
        work_item: { id: 11, title: 'Confluence 연결', status: 'blocked', owner: 'fallback' },
        latest_attempt: { agent_id: 'agent-a' },
        latest_checkpoint: { result: '20개 문서를 확인했다.' },
        blockers: [{ title: '접근 권한 대기' }],
        evidence_references: [{ summary: '문서 목록', reference: 'confluence://atlas/docs' }],
        context_window: { input_bytes: 1536, input_limit_bytes: 4096, truncated: true }
    };

    const summary = monitor.selectedWorkMonitor();
    assert.equal(summary.agent, 'agent-a');
    assert.equal(summary.blocker, '접근 권한 대기');
    assert.equal(summary.result, '20개 문서를 확인했다.');
    assert.equal(summary.evidenceReference, 'confluence://atlas/docs');
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
