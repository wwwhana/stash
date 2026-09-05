(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./search-utils.js'))
        : factory(root.StashSearch);
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root) root.StashGoalMap = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (searchUtils) {
    'use strict';

    if (!searchUtils) throw new Error('검색 모듈을 불러오지 못했습니다.');

    const MIN_CANVAS_WIDTH = 920;
    const MIN_CANVAS_HEIGHT = 720;
    const CANVAS_PADDING = 84;
    const RING_NODE_GAP = 34;
    const FIRST_GOAL_RADIUS = 238;
    const WORK_RING_GAP = 214;
    const CONTEXT_RING_GAP = 200;
    const ROOT_WIDTH = 244;
    const ROOT_HEIGHT = 110;
    const GOAL_WIDTH = 208;
    const GOAL_HEIGHT = 90;
    const WORK_WIDTH = 194;
    const WORK_HEIGHT = 98;
    const RESOURCE_WIDTH = 182;
    const RESOURCE_HEIGHT = 76;
    const MEMORY_WIDTH = 182;
    const MEMORY_HEIGHT = 72;

    function emptyLayout() {
        return { width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], rings: [], focusKey: '', counts: { resource: 0, memory: 0, work: 0, goal: 0 } };
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

    function boundaryPoint(node, toward) {
        const dx = toward.x - node.x;
        const dy = toward.y - node.y;
        if (!dx && !dy) return { x: node.x, y: node.y };
        const horizontal = dx ? (node.width / 2) / Math.abs(dx) : Number.POSITIVE_INFINITY;
        const vertical = dy ? (node.height / 2) / Math.abs(dy) : Number.POSITIVE_INFINITY;
        const scale = Math.min(horizontal, vertical);
        return { x: node.x + dx * scale, y: node.y + dy * scale };
    }

    function edgePath(source, target, index) {
        const start = boundaryPoint(source, target);
        const end = boundaryPoint(target, source);
        const dx = end.x - start.x;
        const dy = end.y - start.y;
        const length = Math.max(1, Math.hypot(dx, dy));
        const bend = Math.min(38, 12 + (index % 4) * 7) * (index % 2 ? 1 : -1);
        const controlX = (start.x + end.x) / 2 - (dy / length) * bend;
        const controlY = (start.y + end.y) / 2 + (dx / length) * bend;
        return `M ${start.x} ${start.y} Q ${controlX} ${controlY} ${end.x} ${end.y}`;
    }

    function dimensions(kind, focus) {
        if (focus) return { width: ROOT_WIDTH, height: ROOT_HEIGHT };
        if (kind === 'goal') return { width: GOAL_WIDTH, height: GOAL_HEIGHT };
        if (kind === 'work') return { width: WORK_WIDTH, height: WORK_HEIGHT };
        if (kind === 'resource') return { width: RESOURCE_WIDTH, height: RESOURCE_HEIGHT };
        return { width: MEMORY_WIDTH, height: MEMORY_HEIGHT };
    }

    function entryKey(kind, item) {
        if (kind === 'goal') return `goal:${item.id}`;
        if (kind === 'work') return `work:${item.id}`;
        return String(item.key);
    }

    function wrapEntries(kind, items) {
        return items.map(item => ({ kind, key: entryKey(kind, item), item })).sort(compareText);
    }

    function requiredRadius(entries) {
        if (!entries.length) return 0;
        const widest = entries.reduce((value, entry) => Math.max(value, dimensions(entry.kind, false).width), 0);
        return (entries.length * (widest + RING_NODE_GAP)) / (Math.PI * 2);
    }

    function startAngle(count) {
        if (count === 1) return 0;
        if (count === 2) return 0;
        return -Math.PI / 2 + Math.PI / count;
    }

    function assignAngles(entries, angleByKey) {
        const first = startAngle(entries.length);
        entries.forEach((entry, index) => {
            entry.angle = first + (Math.PI * 2 * index) / entries.length;
            angleByKey.set(entry.key, entry.angle);
        });
    }

    function connectionAngle(entry, rawEdges, angleByKey) {
        const edge = rawEdges.find(candidate => key(candidate && candidate.from) === entry.key && angleByKey.has(key(candidate && candidate.to)));
        return edge ? angleByKey.get(key(edge.to)) : null;
    }

    function sortByConnection(entries, rawEdges, angleByKey) {
        return entries.sort((left, right) => {
            const leftAngle = connectionAngle(left, rawEdges, angleByKey);
            const rightAngle = connectionAngle(right, rawEdges, angleByKey);
            if (leftAngle !== null && rightAngle !== null && leftAngle !== rightAngle) return leftAngle - rightAngle;
            if (leftAngle !== null) return -1;
            if (rightAngle !== null) return 1;
            return compareText(left, right);
        });
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

    function includesText(values, query) {
        return searchUtils.matchesSearch(values, query);
    }

    function filterGoalMap(rawMap, rawFilters) {
        const goalMap = rawMap && typeof rawMap === 'object' ? rawMap : {};
        const filters = rawFilters && typeof rawFilters === 'object' ? rawFilters : {};
        const kinds = filters.kinds && typeof filters.kinds === 'object' ? filters.kinds : {};
        const query = String(filters.query || '').trim();
        const status = String(filters.status || '').trim();
        const agent = String(filters.agent || '').trim();
        const memoryType = String(filters.memoryType || '').trim();
        const tree = goalMap.goal_tree && typeof goalMap.goal_tree === 'object' ? goalMap.goal_tree : {};

        const workMatches = (item, withQuery) => {
            const expires = Date.parse(item.lease_expires_at || '');
            const displayStatus = item.attempt_status === 'active' && expires <= Date.now() ? 'expired' : item.status;
            if (status && displayStatus !== status) return false;
            const owner = String(item.agent_id || item.owner || '').trim();
            if (agent && owner !== agent) return false;
            return !withQuery || includesText([
                item.issue_key, item.title, item.description, item.status, owner,
                item.reporter, item.issue_type, item.due_at, item.latest_result,
                item.next_action, item.labels, item.required_capabilities
            ], query);
        };
        const entries = [];
        if (kinds.goal !== false) {
            for (const item of Array.isArray(tree.goals) ? tree.goals : []) {
                entries.push({ key: `goal:${item.id}`, kind: 'goal', item, matchesQuery: includesText([
                    item.content, item.status, item.notes, item.parent_id, item.depth
                ], query) });
            }
        }
        if (kinds.work !== false) {
            for (const item of Array.isArray(goalMap.work_items) ? goalMap.work_items : []) {
                if (!workMatches(item, false)) continue;
                entries.push({ key: `work:${item.id}`, kind: 'work', item, matchesQuery: workMatches(item, true) });
            }
        }
        if (kinds.resource !== false) {
            for (const item of Array.isArray(goalMap.resources) ? goalMap.resources : []) {
                entries.push({
                    key: String(item.key), kind: 'resource', item,
                    matchesQuery: includesText([
                        item.title, item.summary, item.source, item.kind, item.authority,
                        item.external_id, item.uri, item.revision
                    ], query)
                });
            }
        }
        if (kinds.memory !== false) {
            for (const item of Array.isArray(goalMap.memories) ? goalMap.memories : []) {
                if (memoryType && item.memory_type !== memoryType) continue;
                entries.push({
                    key: String(item.key), kind: 'memory', item,
                    matchesQuery: includesText([
                        item.content, item.memory_type, item.status, item.source, item.created_at
                    ], query)
                });
            }
        }

        const entryByKey = new Map(entries.map(entry => [entry.key, entry]));
        const allEdges = (Array.isArray(goalMap.edges) ? goalMap.edges : []).filter(edge => (
            entryByKey.has(String(edge && edge.from)) && entryByKey.has(String(edge && edge.to))
        ));
        const activeRelationFilter = Boolean(query || status || agent || memoryType);
        const seedKeys = new Set();
        if (!activeRelationFilter) {
            entries.forEach(entry => seedKeys.add(entry.key));
        } else if (query) {
            entries.filter(entry => entry.matchesQuery).forEach(entry => seedKeys.add(entry.key));
        } else {
            if (status || agent) entries.filter(entry => entry.kind === 'work').forEach(entry => seedKeys.add(entry.key));
            if (memoryType) entries.filter(entry => entry.kind === 'memory').forEach(entry => seedKeys.add(entry.key));
        }

        const visibleKeys = new Set(seedKeys);
        if (activeRelationFilter && seedKeys.size) {
            const outgoing = new Map();
            for (const edge of allEdges) {
                const from = String(edge.from);
                if (!outgoing.has(from)) outgoing.set(from, []);
                outgoing.get(from).push(String(edge.to));
            }
            const queue = Array.from(seedKeys);
            while (queue.length) {
                const current = queue.shift();
                for (const next of outgoing.get(current) || []) {
                    if (visibleKeys.has(next)) continue;
                    visibleKeys.add(next);
                    queue.push(next);
                }
            }
            for (const edge of allEdges) {
                if (seedKeys.has(String(edge.to))) visibleKeys.add(String(edge.from));
            }
        }

        const visibleEntries = entries.filter(entry => visibleKeys.has(entry.key));
        const contextItem = entry => seedKeys.has(entry.key) || !activeRelationFilter
            ? entry.item
            : { ...entry.item, __filter_context: true };
        const goals = visibleEntries.filter(entry => entry.kind === 'goal').map(contextItem);
        const workItems = visibleEntries.filter(entry => entry.kind === 'work').map(contextItem);
        const resources = visibleEntries.filter(entry => entry.kind === 'resource').map(contextItem);
        const memories = visibleEntries.filter(entry => entry.kind === 'memory').map(contextItem);
        const edges = allEdges.filter(edge => (
            visibleKeys.has(String(edge.from)) && visibleKeys.has(String(edge.to))
        ));
        const unassignedWork = kinds.work === false
            ? []
            : (Array.isArray(goalMap.unassigned_work) ? goalMap.unassigned_work : []).filter(item => workMatches(item, Boolean(query)));

        return {
            ...goalMap,
            goal_tree: { ...tree, goals },
            work_items: workItems,
            resources,
            memories,
            edges,
            unassigned_work: unassignedWork
        };
    }

    function buildGoalMapLayout(rawMap) {
        const goalMap = rawMap && typeof rawMap === 'object' ? rawMap : {};
        const goalTree = goalMap.goal_tree && typeof goalMap.goal_tree === 'object' ? goalMap.goal_tree : {};
        const goals = (Array.isArray(goalTree.goals) ? goalTree.goals : []).filter(goal => goal && goal.id !== undefined && goal.id !== null);
        const workItems = (Array.isArray(goalMap.work_items) ? goalMap.work_items : []).filter(item => item && item.id !== undefined && item.id !== null);
        const resources = (Array.isArray(goalMap.resources) ? goalMap.resources : []).filter(resource => resource && resource.key);
        const memories = (Array.isArray(goalMap.memories) ? goalMap.memories : []).filter(memory => memory && memory.key);
        if (!goals.length && !workItems.length && !resources.length && !memories.length) return emptyLayout();

        const rawEdges = Array.isArray(goalMap.edges) ? goalMap.edges : [];
        const rootGoal = goals.find(goal => Number(goal.id) === Number(goalTree.root_goal_id)) || null;
        const focusKey = rootGoal ? `goal:${rootGoal.id}` : '';
        const angleByKey = new Map();
        const ringSpecs = [];
        let previousRadius = 0;

        const childGoals = goals.filter(goal => !rootGoal || Number(goal.id) !== Number(rootGoal.id));
        if (childGoals.length) {
            const goalByID = new Map(goals.map(goal => [Number(goal.id), goal]));
            const hierarchyPath = goal => {
                const parts = [];
                const visited = new Set();
                let current = goal;
                while (current && !visited.has(Number(current.id))) {
                    visited.add(Number(current.id));
                    parts.unshift(String(current.content || current.id));
                    current = goalByID.get(Number(current.parent_id));
                    if (rootGoal && current && Number(current.id) === Number(rootGoal.id)) break;
                }
                return parts.join('\u0000');
            };
            const entries = wrapEntries('goal', childGoals).sort((left, right) => hierarchyPath(left.item).localeCompare(hierarchyPath(right.item), 'ko'));
            assignAngles(entries, angleByKey);
            const radius = Math.max(FIRST_GOAL_RADIUS, requiredRadius(entries));
            ringSpecs.push({ key: 'goal', label: '하위 목표', tone: 'goal', count: entries.length, radius, entries });
            previousRadius = radius;
        }

        if (workItems.length) {
            const entries = sortByConnection(wrapEntries('work', workItems), rawEdges, angleByKey);
            assignAngles(entries, angleByKey);
            const minimum = previousRadius ? previousRadius + WORK_RING_GAP : FIRST_GOAL_RADIUS;
            const radius = Math.max(minimum, requiredRadius(entries));
            ringSpecs.push({ key: 'work', label: '연결 작업', tone: 'work', count: entries.length, radius, entries });
            previousRadius = radius;
        }

        const contextEntries = [
            ...wrapEntries('memory', memories),
            ...wrapEntries('resource', resources)
        ];
        if (contextEntries.length) {
            sortByConnection(contextEntries, rawEdges, angleByKey);
            assignAngles(contextEntries, angleByKey);
            const minimum = previousRadius ? previousRadius + CONTEXT_RING_GAP : FIRST_GOAL_RADIUS;
            const radius = Math.max(minimum, requiredRadius(contextEntries));
            ringSpecs.push({ key: 'context', label: '사실·기억·자료', tone: 'context', count: contextEntries.length, radius, entries: contextEntries });
            previousRadius = radius;
        }

        const outerRadius = ringSpecs.reduce((value, ring) => Math.max(value, ring.radius), 0);
        const halfNodeWidth = Math.max(ROOT_WIDTH, GOAL_WIDTH, WORK_WIDTH, RESOURCE_WIDTH, MEMORY_WIDTH) / 2;
        const halfNodeHeight = Math.max(ROOT_HEIGHT, GOAL_HEIGHT, WORK_HEIGHT, RESOURCE_HEIGHT, MEMORY_HEIGHT) / 2;
        const width = Math.max(MIN_CANVAS_WIDTH, Math.ceil((outerRadius + halfNodeWidth + CANVAS_PADDING) * 2));
        const height = Math.max(MIN_CANVAS_HEIGHT, Math.ceil((outerRadius + halfNodeHeight + CANVAS_PADDING) * 2));
        const centerX = width / 2;
        const centerY = height / 2;
        const nodes = [];

        if (rootGoal) {
            const size = dimensions('goal', true);
            nodes.push({
                kind: 'goal', key: focusKey, item: rootGoal, focus: true,
                x: centerX, y: centerY, width: size.width, height: size.height,
                style: `left:${centerX - size.width / 2}px;top:${centerY - size.height / 2}px;width:${size.width}px;height:${size.height}px`
            });
        }

        const rings = ringSpecs.map(ring => {
            for (const entry of ring.entries) {
                const size = dimensions(entry.kind, false);
                const x = centerX + Math.cos(entry.angle) * ring.radius;
                const y = centerY + Math.sin(entry.angle) * ring.radius;
                nodes.push({
                    ...entry, ringKey: ring.key, x, y, width: size.width, height: size.height,
                    style: `left:${x - size.width / 2}px;top:${y - size.height / 2}px;width:${size.width}px;height:${size.height}px`
                });
            }
            return {
                key: ring.key, label: ring.label, tone: ring.tone, count: ring.count, radius: ring.radius,
                style: `left:${centerX - ring.radius}px;top:${centerY - ring.radius}px;width:${ring.radius * 2}px;height:${ring.radius * 2}px`
            };
        });

        const nodeByKey = new Map(nodes.map(node => [node.key, node]));
        const edges = rawEdges.map((edge, index) => {
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

        return {
            width, height, canvasStyle: `width:${width}px;height:${height}px`, nodes, edges, rings, focusKey,
            counts: { resource: resources.length, memory: memories.length, work: workItems.length, goal: goals.length }
        };
    }

    return { emptyLayout, filterGoalMap, buildGoalMapLayout, relationLabel };
}));
