const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { buildGoalMapLayout, filterGoalMap } = require('./ui/goal-map-layout.js');

function sampleMap() {
    return {
        goal_tree: { root_goal_id: 1, goals: [
            { id: 1, content: 'A', depth: 0, progress: 0.5 },
            { id: 2, parent_id: 1, content: 'A-1', depth: 1, progress: 1 },
            { id: 3, parent_id: 1, content: 'A-2', depth: 1, progress: 0 }
        ] },
        work_items: [{ id: 10, goal_id: 2, issue_key: 'W-10', title: 'A-1 작업', status: 'done', agent_id: 'agent-doc' }],
        resources: [{ key: 'resource:8', id: 8, kind: 'ticket', source: 'jira', authority: 'external', title: '사람 작업' }],
        memories: [{ key: 'memory:fact:5', memory_type: 'fact', memory_id: 5, content: 'A의 제약' }],
        edges: [
            { key: 'r-w', from: 'resource:8', to: 'work:10', relation: 'input' },
            { key: 'm-w', from: 'memory:fact:5', to: 'work:10', relation: 'constraint' },
            { key: 'w-g', from: 'work:10', to: 'goal:2', relation: 'contributes_to' },
            { key: 'g-g', from: 'goal:2', to: 'goal:1', relation: 'contributes_to' }
        ]
    };
}

test('filters keep the matching memory and its path to the shared goal', () => {
    const filtered = filterGoalMap(sampleMap(), {
        query: 'A의 제약', kinds: { goal: true, work: true, memory: true, resource: true }
    });

    assert.deepEqual(filtered.memories.map(item => item.key), ['memory:fact:5']);
    assert.deepEqual(filtered.work_items.map(item => item.id), [10]);
    assert.deepEqual(filtered.goal_tree.goals.map(item => item.id), [1, 2]);
    assert.equal(filtered.resources.length, 0);
    assert.equal(filtered.edges.length, 3);
    assert.equal(filtered.work_items[0].__filter_context, true);
});

test('work status and agent filters keep only related inputs and goals', () => {
    const matched = filterGoalMap(sampleMap(), {
        status: 'done', agent: 'agent-doc', kinds: { goal: true, work: true, memory: true, resource: true }
    });
    const missed = filterGoalMap(sampleMap(), {
        status: 'doing', kinds: { goal: true, work: true, memory: true, resource: true }
    });

    assert.deepEqual(matched.work_items.map(item => item.id), [10]);
    assert.deepEqual(matched.resources.map(item => item.id), [8]);
    assert.deepEqual(matched.memories.map(item => item.memory_id), [5]);
    assert.deepEqual(matched.goal_tree.goals.map(item => item.id), [1, 2]);
    assert.equal(missed.work_items.length, 0);
    assert.equal(missed.resources.length, 0);
    assert.equal(missed.memories.length, 0);
    assert.equal(missed.goal_tree.goals.length, 0);
});

test('goal map search spans structured work metadata and supports multiple tokens', () => {
    const value = sampleMap();
    value.work_items[0].labels = ['지식'];
    value.work_items[0].description = '외부 문서를 확인한다';
    const filtered = filterGoalMap(value, {
        query: '문서 지식', kinds: { goal: true, work: true, memory: true, resource: true }
    });

    assert.deepEqual(filtered.work_items.map(item => item.id), [10]);
    assert.equal(filtered.goal_tree.goals.length, 2);
});

test('node kind and memory type filters are applied before relation expansion', () => {
    const value = sampleMap();
    value.memories.push({ key: 'memory:episode:6', memory_type: 'episode', memory_id: 6, content: '작업 경험' });
    value.edges.push({ key: 'episode-work', from: 'memory:episode:6', to: 'work:10', relation: 'context' });
    const filtered = filterGoalMap(value, {
        memoryType: 'fact', kinds: { goal: true, work: true, memory: true, resource: false }
    });

    assert.deepEqual(filtered.memories.map(item => item.memory_type), ['fact']);
    assert.equal(filtered.resources.length, 0);
    assert.ok(filtered.edges.every(item => item.from !== 'resource:8' && item.to !== 'resource:8'));
});

test('the shared goal is centered and context expands through semantic rings', () => {
    const layout = buildGoalMapLayout(sampleMap());
    const byKey = new Map(layout.nodes.map(node => [node.key, node]));
    const root = byKey.get('goal:1');
    const distance = node => Math.hypot(node.x - root.x, node.y - root.y);

    assert.equal(root.x, layout.width / 2);
    assert.equal(root.y, layout.height / 2);
    assert.ok(distance(byKey.get('goal:2')) < distance(byKey.get('work:10')));
    assert.ok(distance(byKey.get('work:10')) < distance(byKey.get('resource:8')));
    assert.equal(distance(byKey.get('resource:8')), distance(byKey.get('memory:fact:5')));
    assert.equal(layout.edges.length, 4);
    assert.equal(layout.focusKey, 'goal:1');
    assert.deepEqual(layout.rings.map(ring => ring.label), ['하위 목표', '연결 작업', '사실·기억·자료']);
});

test('nested goals stay together and keep their hierarchy edge', () => {
    const value = sampleMap();
    value.goal_tree.goals.push({ id: 4, parent_id: 2, content: 'A-1-1', depth: 2, progress: 0 });
    value.edges.push({ key: 'deep-goal', from: 'goal:4', to: 'goal:2', relation: 'contributes_to' });
    const layout = buildGoalMapLayout(value);
    const byKey = new Map(layout.nodes.map(node => [node.key, node]));
    const root = byKey.get('goal:1');
    const distance = node => Math.hypot(node.x - root.x, node.y - root.y);

    assert.ok(Math.abs(distance(byKey.get('goal:2')) - distance(byKey.get('goal:4'))) < 0.000001);
    assert.ok(distance(byKey.get('goal:4')) < distance(byKey.get('work:10')));
    assert.equal(byKey.get('goal:2').ringKey, 'goal');
    assert.equal(byKey.get('goal:4').ringKey, 'goal');
    assert.ok(layout.edges.some(edge => edge.key === 'deep-goal'));
});

test('unknown endpoints are ignored without dropping valid nodes', () => {
    const value = sampleMap();
    value.edges.push({ key: 'missing', from: 'work:404', to: 'goal:1', relation: 'contributes_to' });
    const layout = buildGoalMapLayout(value);
    assert.equal(layout.nodes.length, 6);
    assert.equal(layout.edges.length, 4);
});

test('empty maps have a stable empty layout', () => {
    assert.deepEqual(buildGoalMapLayout({}), {
        width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], rings: [], focusKey: '', counts: { resource: 0, memory: 0, work: 0, goal: 0 }
    });
});

test('goal map UI keeps resource and monitoring state in its own view-model', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const viewModel = fs.readFileSync(require.resolve('./ui/goal-map-view-model.js'), 'utf8');
    const executionViewModel = fs.readFileSync(require.resolve('./ui/issue-execution-view-model.js'), 'utf8');

    assert.match(viewModel, /resources: \[\]/);
    assert.match(viewModel, /goalMapAttentionItems\(\)/);
    assert.match(viewModel, /goalMapFilters:/);
    assert.match(viewModel, /refreshGoalMapLayout\(\)/);
    assert.match(viewModel, /goalMapAgents\(\)/);
    assert.match(viewModel, /required_capabilities/);
    assert.match(viewModel, /invokeTool\('get_goal_map'/);
    assert.match(html, /node\.kind === 'resource'/);
    assert.match(html, />연결 자료</);
    assert.match(html, /aria-label="목표·지식 지도 필터"/);
    assert.match(html, /aria-controls="goal-map-filter-menu"/);
    assert.match(html, /goalMapFilterChips\(\)/);
    assert.match(viewModel, /goalMapFilterOpen: false/);
    assert.match(viewModel, /clearGoalMapFilter\(/);
    assert.match(html, /x-for="ring in goalMapLayout\.rings"/);
    assert.doesNotMatch(html, /goalMapLayout\.columns|stash-goal-map__column/);
    assert.match(html, /@submit\.prevent="claimWork"/);
    assert.match(executionViewModel, /runExecutionMutation\('claim_work'/);
    assert.match(html, /@media \(max-width: 680px\)[\s\S]*?\.stash-goal-map__summary \{ flex-wrap: wrap; \}/);
});

test('goal map colors use the shared theme palette', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const styles = html.match(/\.stash-goal-map \{[\s\S]*?\.stash-goal-map__unassigned button \{[^}]+\}/)?.[0] || '';
    assert.match(styles, /var\(--stash-surface\)/);
    assert.match(styles, /var\(--stash-ink\)/);
    assert.doesNotMatch(styles, /(?:background|color|border-color):\s*#(?:fff|ffffff)\b/i);

    const graphStyles = fs.readFileSync(require.resolve('./ui/work-graph-board.css'), 'utf8');
    assert.doesNotMatch(graphStyles, /^:root\s*\{/m);
});

test('the work plan keeps project scope separate from map and memory scope', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const viewModel = fs.readFileSync(require.resolve('./ui/work-plan-view-model.js'), 'utf8');

    assert.match(html, /x-model="planNamespaceSlug" @change="loadWorkPlan\(false\)"/);
    assert.match(html, /x-for="namespace in planProjects\(\)"/);
    assert.doesNotMatch(html, /x-model="planNamespaceSlug"[\s\S]{0,300}>기본 공간/);
    assert.match(viewModel, /planNamespaceSlug: ''/);
    assert.match(viewModel, /\^\\\/projects\\\/\[\^\/\]\+\$/);
    assert.match(viewModel, /const mapProject = projects\.find\(item => item\.slug === this\.mapNamespaceSlug\)/);
    assert.match(viewModel, /planNamespace\(\)/);
    assert.doesNotMatch(viewModel, /return this\.mapNamespaceSlug \|\| '\/'/);
    assert.match(viewModel, /const namespace = this\.planNamespace\(\)/);
    assert.match(viewModel, /get_work_plan', \{ namespace \}/);
    assert.match(viewModel, /validate_work_plan', \{ namespace: this\.planNamespace\(\) \}/);
    assert.match(viewModel, /namespace: this\.planNamespace\(\)/);
    assert.match(html, /class="stash-plan-toolbar"[\s\S]*class="stash-plan-summary"/);
    assert.doesNotMatch(html, /stash-plan-intro/);
    assert.match(html, />맡는 범위</);
    assert.doesNotMatch(html, /5~9개/);
});

test('namespace selection is shared by relation views and memory lists', () => {
    const html = fs.readFileSync(require.resolve('./ui/index.html'), 'utf8');
    const scopeViewModel = fs.readFileSync(require.resolve('./ui/map-scope-view-model.js'), 'utf8');

    assert.match(scopeViewModel, /mapNamespaces: \[\]/);
    assert.match(scopeViewModel, /mapNamespaceSlug: ''/);
    assert.match(scopeViewModel, /invokeTool\('list_namespaces'/);
    assert.match(scopeViewModel, /listed\.push\(\.\.\.result\.items\)/);
    assert.match(html, /x-model="mapNamespaceSlug" @change="loadGoalMap\(false\)"/);
    assert.match(html, /x-model="mapNamespaceSlug" @change="changeWorkGraphNamespace\(\)"/);
    assert.match(html, /query_facts', \{namespaces: mapNamespaceSlug \|\| '\/'/);
});
