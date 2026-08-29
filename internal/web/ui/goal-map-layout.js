(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root) root.StashGoalMap = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const PADDING_X = 24;
    const PADDING_TOP = 58;
    const PADDING_BOTTOM = 24;
    const COLUMN_GAP = 52;
    const ROW_GAP = 18;
    const RESOURCE_WIDTH = 190;
    const RESOURCE_HEIGHT = 80;
    const MEMORY_WIDTH = 190;
    const MEMORY_HEIGHT = 74;
    const WORK_WIDTH = 194;
    const WORK_HEIGHT = 98;
    const GOAL_WIDTH = 220;
    const GOAL_HEIGHT = 96;

    function emptyLayout() {
        return { width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], columns: [], counts: { resource: 0, memory: 0, work: 0, goal: 0 } };
    }

    function key(value) {
        return String(value === undefined || value === null ? '' : value);
    }

    function nodeLabel(node) {
        if (!node) return '';
        if (node.kind === 'goal') return String(node.item.content || `목표 #${node.item.id}`);
        if (node.kind === 'work') return String(node.item.title || node.item.issue_key || `작업 #${node.item.id}`);
        if (node.kind === 'resource') return String(node.item.title || `${node.item.source || '자료'} #${node.item.id}`);
        return String(node.item.content || `${node.item.memory_type} #${node.item.memory_id}`);
    }

    function compareText(left, right) {
        return nodeLabel(left).localeCompare(nodeLabel(right), 'ko');
    }

    function edgePath(source, target, index) {
        const sourceX = source.x + (target.x >= source.x ? source.width / 2 : -source.width / 2);
        const targetX = target.x + (target.x >= source.x ? -target.width / 2 : target.width / 2);
        const control = Math.max(38, Math.abs(targetX - sourceX) * 0.42) + (index % 3) * 6;
        const direction = targetX >= sourceX ? 1 : -1;
        return `M ${sourceX} ${source.y} C ${sourceX + direction * control} ${source.y}, ${targetX - direction * control} ${target.y}, ${targetX} ${target.y}`;
    }

    function relationStyle(relation) {
        switch (relation) {
        case 'contributes_to': return { stroke: '#818cf8', dashArray: null, marker: true };
        case 'blocks': return { stroke: '#fb7185', dashArray: null, marker: true };
        case 'input': case 'target': case 'reference': return { stroke: '#38bdf8', dashArray: '4 4', marker: true };
        case 'output': return { stroke: '#22d3ee', dashArray: null, marker: true };
        case 'result': case 'evidence': case 'supersedes': return { stroke: '#34d399', dashArray: '6 5', marker: true };
        case 'part_of': return { stroke: '#94a3b8', dashArray: '5 5', marker: false };
        default: return { stroke: '#fbbf24', dashArray: '6 5', marker: true };
        }
    }

    function relationLabel(relation) {
        return ({
            contributes_to: '결과가 목표에 합쳐짐', blocks: '먼저 끝나야 함', relates_to: '관련 있음',
            part_of: '상위 작업에 포함됨', context: '배경을 제공함', constraint: '제한 조건을 제공함',
            decision: '결정 근거를 제공함', failure: '실패 경험을 제공함', evidence: '확인 결과를 남김',
            result: '결과를 남김', supersedes: '이전 기억을 바꿈', input: '입력 자료로 사용함',
            target: '수정 대상을 가리킴', reference: '참고 자료로 연결됨', output: '작업 결과로 남김'
        })[relation] || relation;
    }

    function buildGoalMapLayout(rawMap) {
        const goalMap = rawMap && typeof rawMap === 'object' ? rawMap : {};
        const goalTree = goalMap.goal_tree && typeof goalMap.goal_tree === 'object' ? goalMap.goal_tree : {};
        const goals = (Array.isArray(goalTree.goals) ? goalTree.goals : []).filter(goal => goal && goal.id !== undefined && goal.id !== null);
        const workItems = (Array.isArray(goalMap.work_items) ? goalMap.work_items : []).filter(item => item && item.id !== undefined && item.id !== null);
        const resources = (Array.isArray(goalMap.resources) ? goalMap.resources : []).filter(resource => resource && resource.key);
        const memories = (Array.isArray(goalMap.memories) ? goalMap.memories : []).filter(memory => memory && memory.key);
        if (!goals.length && !workItems.length && !resources.length && !memories.length) return emptyLayout();

        const maxDepth = goals.reduce((value, goal) => Math.max(value, Number(goal.depth) || 0), 0);
        const columnSpecs = [];
        if (resources.length) columnSpecs.push({ key: 'resource', kind: 'resource', label: '연결 자료', width: RESOURCE_WIDTH, items: resources });
        if (memories.length) columnSpecs.push({ key: 'memory', kind: 'memory', label: '기억', width: MEMORY_WIDTH, items: memories });
        if (workItems.length) columnSpecs.push({ key: 'work', kind: 'work', label: '작업', width: WORK_WIDTH, items: workItems });
        for (let depth = maxDepth; depth >= 0; depth -= 1) {
            const items = goals.filter(goal => (Number(goal.depth) || 0) === depth);
            if (!items.length) continue;
            columnSpecs.push({
                key: `goal-${depth}`, kind: 'goal', depth, width: GOAL_WIDTH, items,
                label: depth === 0 ? '공통 목표' : (depth === maxDepth ? '세부 목표' : `${depth}단계 목표`)
            });
        }

        let cursorX = PADDING_X;
        const columns = [];
        const nodes = [];
        for (const spec of columnSpecs) {
            const heightForKind = spec.kind === 'goal' ? GOAL_HEIGHT : (spec.kind === 'work' ? WORK_HEIGHT : (spec.kind === 'resource' ? RESOURCE_HEIGHT : MEMORY_HEIGHT));
            const wrapped = spec.items.map(item => ({
                kind: spec.kind,
                key: spec.kind === 'goal' ? `goal:${item.id}` : (spec.kind === 'work' ? `work:${item.id}` : String(item.key)),
                item
            })).sort(compareText);
            const columnX = cursorX + spec.width / 2;
            wrapped.forEach((entry, index) => {
                const y = PADDING_TOP + heightForKind / 2 + index * (heightForKind + ROW_GAP);
                nodes.push({
                    ...entry, x: columnX, y, width: spec.width, height: heightForKind,
                    style: `left:${columnX - spec.width / 2}px;top:${y - heightForKind / 2}px;width:${spec.width}px;height:${heightForKind}px`
                });
            });
            columns.push({
                key: spec.key, label: spec.label, count: wrapped.length,
                style: `left:${cursorX}px;width:${spec.width}px`
            });
            cursorX += spec.width + COLUMN_GAP;
        }

        const nodeByKey = new Map(nodes.map(node => [node.key, node]));
        const edges = (Array.isArray(goalMap.edges) ? goalMap.edges : []).map((edge, index) => {
            const source = nodeByKey.get(key(edge && edge.from));
            const target = nodeByKey.get(key(edge && edge.to));
            if (!source || !target) return null;
            const relation = String(edge.relation || 'relates_to');
            const style = relationStyle(relation);
            return {
                key: String(edge.key || `goal-map-edge-${index}`), relation,
                path: edgePath(source, target, index), ...style,
                ariaLabel: `${nodeLabel(source)}에서 ${nodeLabel(target)}까지: ${relationLabel(relation)}`
            };
        }).filter(Boolean);

        const rowCount = Math.max(1, ...columnSpecs.map(spec => spec.items.length));
        const rowHeight = Math.max(RESOURCE_HEIGHT, MEMORY_HEIGHT, WORK_HEIGHT, GOAL_HEIGHT);
        const height = PADDING_TOP + rowCount * rowHeight + Math.max(0, rowCount - 1) * ROW_GAP + PADDING_BOTTOM;
        const width = Math.max(360, cursorX - COLUMN_GAP + PADDING_X);
        return {
            width, height, canvasStyle: `width:${width}px;height:${height}px`, nodes, edges, columns,
            counts: { resource: resources.length, memory: memories.length, work: workItems.length, goal: goals.length }
        };
    }

    return { emptyLayout, buildGoalMapLayout, relationLabel };
}));
