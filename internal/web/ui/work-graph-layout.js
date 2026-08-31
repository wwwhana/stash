(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root) root.StashWorkGraph = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    // Keep the board generous enough for readable cards and visible relation lanes.
    // The columns still come from dependency depth; these values only control the
    // amount of breathing room around that topology.
    const NODE_WIDTH = 236;
    const NODE_HEIGHT = 132;
    const COLUMN_WIDTH = 276;
    const COLUMN_GAP = 96;
    const ROW_GAP = 36;
    const PADDING_X = 64;
    const PADDING_TOP = 56;
    const PADDING_BOTTOM = 56;
    const MIN_WIDTH = 760;
    const MIN_HEIGHT = 360;

    function keyOf(value) {
        return String(value);
    }

    function nodeName(node) {
        return String(node && (node.issue_key || node.title || ('#' + node.id)) || '작업');
    }

    function compareNodes(left, right) {
        const byName = nodeName(left).localeCompare(nodeName(right), 'ko');
        if (byName !== 0) return byName;
        return keyOf(left && left.id).localeCompare(keyOf(right && right.id));
    }

    function dragKey(key) {
        return `node:${key}`;
    }

    function nodeOffset(offsets, key) {
        const value = offsets && offsets[key];
        const x = Number(value && value.x);
        const y = Number(value && value.y);
        return {
            x: Number.isFinite(x) ? x : 0,
            y: Number.isFinite(y) ? y : 0
        };
    }

    function normalizeNodes(rawNodes) {
        const seen = new Set();
        return (Array.isArray(rawNodes) ? rawNodes : []).filter(node => {
            if (!node || node.id === undefined || node.id === null) return false;
            const key = keyOf(node.id);
            if (seen.has(key)) return false;
            seen.add(key);
            return true;
        });
    }

    function normalizeEdges(rawEdges, nodeMap) {
        return (Array.isArray(rawEdges) ? rawEdges : []).map((edge, index) => ({
            edge,
            index,
            key: edge && edge.id !== undefined && edge.id !== null
                ? `work-edge-${edge.id}`
                : `work-edge-${index}`,
            fromKey: keyOf(edge && edge.from_item_id),
            toKey: keyOf(edge && edge.to_item_id),
            type: String(edge && edge.edge_type || 'relates_to')
        })).filter(edge => nodeMap.has(edge.fromKey) && nodeMap.has(edge.toKey));
    }

    function countMetric(metrics, name, amount = 1) {
        if (!metrics) return;
        metrics[name] = (Number(metrics[name]) || 0) + amount;
    }

    function buildHierarchy(nodes, nodeMap, metrics) {
        const parentByNode = new Map();
        const orphanParentByNode = new Map();
        const hierarchyCycleNodes = new Set();

        for (const node of nodes) {
            if (node.parent_id === undefined || node.parent_id === null) continue;
            const key = keyOf(node.id);
            const parentKey = keyOf(node.parent_id);
            if (!nodeMap.has(parentKey)) {
                orphanParentByNode.set(key, node.parent_id);
                continue;
            }
            if (parentKey === key) {
                hierarchyCycleNodes.add(key);
                continue;
            }
            parentByNode.set(key, parentKey);
        }

        const resolved = new Set();
        for (const node of nodes) {
            const start = keyOf(node.id);
            if (resolved.has(start)) continue;
            const chain = [];
            const indexByKey = new Map();
            let current = start;
            while (parentByNode.has(current) && !resolved.has(current)) {
                countMetric(metrics, 'hierarchySteps');
                if (indexByKey.has(current)) {
                    const members = chain.slice(indexByKey.get(current));
                    members.forEach(key => hierarchyCycleNodes.add(key));
                    const breakKey = members.slice().sort((left, right) => (
                        compareNodes(nodeMap.get(left), nodeMap.get(right))
                    ))[0];
                    parentByNode.delete(breakKey);
                    break;
                }
                indexByKey.set(current, chain.length);
                chain.push(current);
                current = parentByNode.get(current);
            }
            chain.forEach(key => resolved.add(key));
        }

        const childrenByParent = new Map(nodes.map(node => [keyOf(node.id), []]));
        for (const [childKey, parentKey] of parentByNode) {
            childrenByParent.get(parentKey).push(childKey);
        }
        for (const children of childrenByParent.values()) {
            children.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));
        }
        return { parentByNode, childrenByParent, orphanParentByNode, hierarchyCycleNodes };
    }

    function minHeap(compare) {
        const values = [];
        return {
            get length() { return values.length; },
            push(value) {
                values.push(value);
                let index = values.length - 1;
                while (index > 0) {
                    const parent = Math.floor((index - 1) / 2);
                    if (compare(values[parent], value) <= 0) break;
                    values[index] = values[parent];
                    index = parent;
                }
                values[index] = value;
            },
            pop() {
                const first = values[0];
                const last = values.pop();
                if (values.length && last !== undefined) {
                    let index = 0;
                    while (true) {
                        const left = index * 2 + 1;
                        if (left >= values.length) break;
                        const right = left + 1;
                        const child = right < values.length && compare(values[right], values[left]) < 0 ? right : left;
                        if (compare(values[child], last) >= 0) break;
                        values[index] = values[child];
                        index = child;
                    }
                    values[index] = last;
                }
                return first;
            }
        };
    }

    function relationGroup(type) {
        if (type === 'part_of' || type === 'blocks') return type;
        return 'relates_to';
    }

    function relationEnabled(relations, type) {
        return !relations || relations[relationGroup(type)] !== false;
    }

    function graphEdges(explicitEdges, hierarchy, relations) {
        const result = explicitEdges.slice();
        if (!relationEnabled(relations, 'part_of')) return result;
        for (const [childKey, parentKey] of hierarchy.parentByNode) {
            result.push({
                edge: null,
                index: result.length,
                key: `work-parent-${childKey}-${parentKey}`,
                fromKey: childKey,
                toKey: parentKey,
                type: 'part_of'
            });
        }
        return result;
    }

    function stronglyConnectedComponents(nodeKeys, outgoing, reverseOutgoing) {
        const visited = new Set();
        const finished = [];
        for (const start of nodeKeys) {
            if (visited.has(start)) continue;
            visited.add(start);
            const stack = [{ key: start, next: 0 }];
            while (stack.length) {
                const frame = stack[stack.length - 1];
                const neighbors = outgoing.get(frame.key) || [];
                if (frame.next < neighbors.length) {
                    const next = neighbors[frame.next++];
                    if (!visited.has(next)) {
                        visited.add(next);
                        stack.push({ key: next, next: 0 });
                    }
                    continue;
                }
                finished.push(frame.key);
                stack.pop();
            }
        }

        const assigned = new Set();
        const components = [];
        for (let index = finished.length - 1; index >= 0; index -= 1) {
            const start = finished[index];
            if (assigned.has(start)) continue;
            const members = [];
            const stack = [start];
            assigned.add(start);
            while (stack.length) {
                const current = stack.pop();
                members.push(current);
                for (const previous of reverseOutgoing.get(current) || []) {
                    if (assigned.has(previous)) continue;
                    assigned.add(previous);
                    stack.push(previous);
                }
            }
            components.push(members);
        }
        return components;
    }

    function orderColumns(columns, directedEdges, nodeMap, metrics) {
        const incoming = new Map();
        directedEdges.forEach(edge => {
            if (!incoming.has(edge.toKey)) incoming.set(edge.toKey, []);
            incoming.get(edge.toKey).push(edge.fromKey);
        });
        const position = new Map();
        columns.forEach((members, depth) => {
            members.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));
            if (depth > 0) {
                const averageByNode = new Map(members.map(key => {
                    const values = (incoming.get(key) || []).map(parent => position.get(parent)).filter(Number.isFinite);
                    countMetric(metrics, 'orderingIncomingVisits', (incoming.get(key) || []).length);
                    return [key, values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : Number.POSITIVE_INFINITY];
                }));
                members.sort((left, right) => {
                    const delta = averageByNode.get(left) - averageByNode.get(right);
                    return Number.isFinite(delta) && delta !== 0
                        ? delta
                        : compareNodes(nodeMap.get(left), nodeMap.get(right));
                });
            }
            members.forEach((key, index) => position.set(key, index));
        });
        return columns;
    }

    function columnYPositions(columns, directedEdges, metrics) {
        const spacing = NODE_HEIGHT + ROW_GAP;
        const maxRows = Math.max(1, ...columns.map(column => column.length));
        const minY = PADDING_TOP + NODE_HEIGHT / 2;
        const maxY = minY + Math.max(0, maxRows - 1) * spacing;
        const depthByNode = new Map();
        columns.forEach((members, depth) => members.forEach(key => depthByNode.set(key, depth)));
        const incoming = new Map();
        directedEdges.forEach(edge => {
            if (!incoming.has(edge.toKey)) incoming.set(edge.toKey, []);
            incoming.get(edge.toKey).push(edge.fromKey);
        });
        const positions = new Map();

        columns.forEach((members, depth) => {
            const centeredStart = minY + Math.max(0, maxRows - members.length) * spacing / 2;
            if (depth === 0) {
                members.forEach((key, index) => positions.set(key, centeredStart + index * spacing));
                return;
            }

            const desired = members.map((key, index) => {
                const sources = (incoming.get(key) || []).filter(source => (
                    (depthByNode.get(source) || 0) < depth && Number.isFinite(positions.get(source))
                ));
                countMetric(metrics, 'verticalIncomingVisits', (incoming.get(key) || []).length);
                if (!sources.length) return centeredStart + index * spacing;
                return sources.reduce((sum, source) => sum + positions.get(source), 0) / sources.length;
            });

            // Isotonic spacing keeps cards apart while staying as close as
            // possible to the average position of their incoming branches.
            const blocks = [];
            desired.forEach((value, index) => {
                blocks.push({ start: index, end: index, sum: value - index * spacing, count: 1 });
                while (blocks.length > 1) {
                    const right = blocks[blocks.length - 1];
                    const left = blocks[blocks.length - 2];
                    if (left.sum / left.count <= right.sum / right.count) break;
                    blocks.splice(blocks.length - 2, 2, {
                        start: left.start,
                        end: right.end,
                        sum: left.sum + right.sum,
                        count: left.count + right.count
                    });
                }
            });
            const fitted = new Array(members.length);
            blocks.forEach(block => {
                const average = block.sum / block.count;
                for (let index = block.start; index <= block.end; index += 1) {
                    fitted[index] = average + index * spacing;
                }
            });
            let shift = 0;
            if (fitted.length && fitted[0] < minY) shift = minY - fitted[0];
            if (fitted.length && fitted[fitted.length - 1] + shift > maxY) {
                shift += maxY - (fitted[fitted.length - 1] + shift);
            }
            members.forEach((key, index) => positions.set(key, fitted[index] + shift));
        });
        return positions;
    }

    function computeLevels(nodeKeys, directedEdges, nodeMap, metrics) {
        if (!nodeKeys.length) return { columns: [], cycles: [], cycleByNode: new Map(), maxDepth: -1 };
        const outgoing = new Map(nodeKeys.map(key => [key, []]));
        const reverseOutgoing = new Map(nodeKeys.map(key => [key, []]));
        const selfLoops = new Set();
        directedEdges.forEach(edge => {
            countMetric(metrics, 'topologyEdgeVisits');
            outgoing.get(edge.fromKey).push(edge.toKey);
            reverseOutgoing.get(edge.toKey).push(edge.fromKey);
            if (edge.fromKey === edge.toKey) selfLoops.add(edge.fromKey);
        });

        const components = stronglyConnectedComponents(nodeKeys, outgoing, reverseOutgoing);
        components.forEach(component => component.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right))));
        const componentByNode = new Map();
        components.forEach((component, index) => component.forEach(key => componentByNode.set(key, index)));

        const componentOutgoing = components.map(() => new Set());
        const indegree = components.map(() => 0);
        directedEdges.forEach(edge => {
            countMetric(metrics, 'topologyEdgeVisits');
            const from = componentByNode.get(edge.fromKey);
            const to = componentByNode.get(edge.toKey);
            if (from === to || componentOutgoing[from].has(to)) return;
            componentOutgoing[from].add(to);
            indegree[to] += 1;
        });

        const componentNames = components.map(component => nodeName(nodeMap.get(component[0])));
        const queue = minHeap((left, right) => {
            countMetric(metrics, 'queueComparisons');
            const byName = componentNames[left].localeCompare(componentNames[right], 'ko');
            return byName || left - right;
        });
        const depth = components.map(() => 0);
        indegree.forEach((value, index) => { if (value === 0) queue.push(index); });
        while (queue.length) {
            const current = queue.pop();
            for (const next of componentOutgoing[current]) {
                depth[next] = Math.max(depth[next], depth[current] + 1);
                indegree[next] -= 1;
                if (indegree[next] === 0) queue.push(next);
            }
        }

        const cycles = [];
        const cycleByNode = new Map();
        components.forEach((component, index) => {
            if (component.length === 1 && !selfLoops.has(component[0])) return;
            const cycle = {
                id: `cycle-${cycles.length + 1}`,
                nodeIds: component.map(key => nodeMap.get(key).id),
                label: component.map(key => nodeName(nodeMap.get(key))).join(' ↔ ')
            };
            cycles.push(cycle);
            component.forEach(key => cycleByNode.set(key, cycle));
        });

        const maxDepth = Math.max(...depth, 0);
        const columns = Array.from({ length: maxDepth + 1 }, () => []);
        components.forEach((component, index) => component.forEach(key => columns[depth[index]].push(key)));
        orderColumns(columns, directedEdges, nodeMap, metrics);
        return { columns, cycles, cycleByNode, maxDepth };
    }

    function edgePath(source, target, index) {
        const sourceCenterX = source.x;
        const targetCenterX = target.x;
        const sameColumn = source.depth === target.depth;

        // Links that stay in one dependency column use a quiet outside lane. This
        // keeps related/cyclic links from cutting through a card stack and makes
        // the direction of a cycle legible without changing the graph topology.
        if (sameColumn || Math.abs(targetCenterX - sourceCenterX) < 1) {
            const sourceX = source.x + source.width / 2;
            const targetX = target.x + target.width / 2;
            const lane = Math.max(sourceX, targetX) + 52 + (index % 4) * 16;
            if (source === target) {
                const lift = 30 + (index % 3) * 10;
                return `M ${sourceX} ${source.y - 12} C ${lane} ${source.y - lift}, ${lane} ${source.y + lift}, ${sourceX} ${source.y + 12}`;
            }
            return `M ${sourceX} ${source.y} C ${lane} ${source.y}, ${lane} ${target.y}, ${targetX} ${target.y}`;
        }

        // Anchor each path at the facing card edges. A longer, eased control span
        // leaves a clear lane between columns even after a node is moved manually.
        const direction = targetCenterX >= sourceCenterX ? 1 : -1;
        const sourceX = source.x + direction * source.width / 2;
        const targetX = target.x - direction * target.width / 2;
        const distance = Math.abs(targetX - sourceX);
        const control = Math.max(56, distance * 0.48) + (index % 3) * 8;
        return `M ${sourceX} ${source.y} C ${sourceX + direction * control} ${source.y}, ${targetX - direction * control} ${target.y}, ${targetX} ${target.y}`;
    }

    function edgeAppearance(type, cycle) {
        if (cycle) return { stroke: '#db6b67', dashArray: null, marker: true, tone: 'cycle' };
        if (type === 'blocks') return { stroke: '#e9a23b', dashArray: null, marker: true, tone: 'blocks' };
        if (type === 'part_of') return { stroke: '#9b8cf2', dashArray: null, marker: true, tone: 'part-of' };
        return { stroke: '#7d8798', dashArray: '7 6', marker: false, tone: 'relates' };
    }

    function edgeLabel(edge, source, target) {
        if (edge.type === 'blocks') return `${nodeName(source.item)}가 끝나야 ${nodeName(target.item)}를 진행할 수 있습니다.`;
        if (edge.type === 'part_of') return `${nodeName(source.item)}의 결과가 ${nodeName(target.item)}에 합쳐집니다.`;
        return `${nodeName(source.item)}과 ${nodeName(target.item)}는 관련된 작업입니다.`;
    }

    function emptyLayout() {
        return {
            width: 0,
            height: 0,
            canvasStyle: '',
            nodes: [],
            edges: [],
            disconnected: [],
            cycles: [],
            hierarchyWarnings: [],
            maxDepth: -1,
            sourceNodeCount: 0,
            visibleNodeCount: 0
        };
    }

    function buildWorkGraphLayout(rawNodes, rawEdges, options) {
        const nodes = normalizeNodes(rawNodes);
        if (!nodes.length) return emptyLayout();
        const nodeMap = new Map(nodes.map(node => [keyOf(node.id), node]));
        const relations = options && options.relations && typeof options.relations === 'object' ? options.relations : null;
        const topologyEdges = normalizeEdges(rawEdges, nodeMap);
        const explicitEdges = topologyEdges.filter(edge => relationEnabled(relations, edge.type));
        const metrics = options && options.metrics && typeof options.metrics === 'object' ? options.metrics : null;
        const hierarchy = buildHierarchy(nodes, nodeMap, metrics);
        const edges = graphEdges(explicitEdges, hierarchy, relations);
        const nodeKeys = nodes.map(node => keyOf(node.id));
        // Both dependency and containment links shape the map. A child flows into
        // its parent just as a prerequisite flows into its successor. Relation
        // filters only change what is drawn; they must not make the board jump.
        const placementEdges = topologyEdges.filter(edge => edge.type === 'blocks');
        for (const [childKey, parentKey] of hierarchy.parentByNode) {
            placementEdges.push({
                edge: null,
                index: placementEdges.length,
                key: `work-parent-${childKey}-${parentKey}`,
                fromKey: childKey,
                toKey: parentKey,
                type: 'part_of'
            });
        }
        const placementData = computeLevels(nodeKeys, placementEdges, nodeMap, metrics);
        // A containment link can oppose a dependency without meaning that the
        // two jobs block each other. Cycle badges therefore come only from the
        // actual blocking graph.
        const blockingData = computeLevels(
            nodeKeys,
            topologyEdges.filter(edge => edge.type === 'blocks'),
            nodeMap,
            null
        );
        const levelData = {
            ...placementData,
            cycles: blockingData.cycles,
            cycleByNode: blockingData.cycleByNode
        };
        const yByNode = columnYPositions(levelData.columns, placementEdges, metrics);
        const offsets = options && options.offsets && typeof options.offsets === 'object' ? options.offsets : {};

        const contentWidth = PADDING_X * 2 + levelData.columns.length * COLUMN_WIDTH + Math.max(0, levelData.columns.length - 1) * COLUMN_GAP;
        const maxRows = Math.max(1, ...levelData.columns.map(column => column.length));
        const contentHeight = PADDING_TOP + maxRows * NODE_HEIGHT + Math.max(0, maxRows - 1) * ROW_GAP + PADDING_BOTTOM;
        const baseWidth = Math.max(MIN_WIDTH, contentWidth);
        const baseHeight = Math.max(MIN_HEIGHT, contentHeight);
        const allOffsets = nodeKeys.map(key => nodeOffset(offsets, dragKey(key)));
        const width = baseWidth + Math.max(0, ...allOffsets.map(offset => offset.x));
        const height = baseHeight + Math.max(0, ...allOffsets.map(offset => offset.y));
        const contentInset = Math.max(0, (baseWidth - contentWidth) / 2);
        const layoutByNode = new Map();
        const incoming = new Map(nodeKeys.map(key => [key, 0]));
        const outgoing = new Map(nodeKeys.map(key => [key, 0]));
        topologyEdges.filter(edge => edge.type === 'blocks').forEach(edge => {
            countMetric(metrics, 'topologyEdgeVisits');
            outgoing.set(edge.fromKey, (outgoing.get(edge.fromKey) || 0) + 1);
            incoming.set(edge.toKey, (incoming.get(edge.toKey) || 0) + 1);
        });
        const layoutNodes = [];

        levelData.columns.forEach((members, depth) => {
            const left = contentInset + PADDING_X + depth * (COLUMN_WIDTH + COLUMN_GAP);
            members.forEach((key, row) => {
                const item = nodeMap.get(key);
                const offset = nodeOffset(offsets, dragKey(key));
                const baseX = left + COLUMN_WIDTH / 2;
                const baseY = yByNode.get(key);
                const x = baseX + offset.x;
                const y = baseY + offset.y;
                const parentKey = hierarchy.parentByNode.get(key);
                const children = hierarchy.childrenByParent.get(key) || [];
                const layoutNode = {
                    key,
                    item,
                    dragKey: dragKey(key),
                    offset,
                    baseX,
                    baseY,
                    x,
                    y,
                    width: NODE_WIDTH,
                    height: NODE_HEIGHT,
                    canvasWidth: width,
                    canvasHeight: height,
                    depth,
                    isEntry: (incoming.get(key) || 0) === 0 && (outgoing.get(key) || 0) > 0,
                    isOutcome: (incoming.get(key) || 0) > 0 && (outgoing.get(key) || 0) === 0,
                    parentItem: parentKey ? nodeMap.get(parentKey) : null,
                    childItems: children.map(childKey => nodeMap.get(childKey)),
                    orphanParentId: hierarchy.orphanParentByNode.get(key),
                    hierarchyCycle: hierarchy.hierarchyCycleNodes.has(key),
                    cycle: levelData.cycleByNode.get(key) || null,
                    context: Boolean(item && item.__filter_context),
                    style: `left:${x - NODE_WIDTH / 2}px;top:${y - NODE_HEIGHT / 2}px;width:${NODE_WIDTH}px;height:${NODE_HEIGHT}px`
                };
                layoutNodes.push(layoutNode);
                layoutByNode.set(key, layoutNode);
            });
        });

        const layoutEdges = edges.map((edge, index) => {
            const source = layoutByNode.get(edge.fromKey);
            const target = layoutByNode.get(edge.toKey);
            if (!source || !target) return null;
            const sourceCycle = levelData.cycleByNode.get(edge.fromKey);
            const cycle = sourceCycle && sourceCycle === levelData.cycleByNode.get(edge.toKey);
            return {
                key: edge.key,
                fromKey: edge.fromKey,
                toKey: edge.toKey,
                type: edge.type,
                cycle: Boolean(cycle),
                path: edgePath(source, target, index),
                ...edgeAppearance(edge.type, cycle),
                ariaLabel: edgeLabel(edge, source, target)
            };
        }).filter(Boolean);

        const incident = new Set();
        edges.forEach(edge => { incident.add(edge.fromKey); incident.add(edge.toKey); });
        const disconnected = layoutNodes.filter(node => !incident.has(node.key));
        const hierarchyWarnings = nodes.filter(node => {
            const key = keyOf(node.id);
            return hierarchy.orphanParentByNode.has(key) || hierarchy.hierarchyCycleNodes.has(key);
        }).map(node => {
            const key = keyOf(node.id);
            const orphanParentId = hierarchy.orphanParentByNode.get(key);
            return {
                key,
                item: node,
                label: orphanParentId !== undefined
                    ? `${nodeName(node)}: 상위 작업 #${orphanParentId}을 찾을 수 없습니다.`
                    : `${nodeName(node)}: 상위 작업 연결이 순환합니다.`
            };
        });

        return {
            width,
            height,
            canvasStyle: `width:${width}px;height:${height}px`,
            nodes: layoutNodes,
            edges: layoutEdges,
            disconnected,
            cycles: levelData.cycles,
            hierarchyWarnings,
            maxDepth: levelData.maxDepth,
            sourceNodeCount: Number(options && options.sourceNodeCount) || nodes.length,
            visibleNodeCount: nodes.length
        };
    }

    return { buildWorkGraphLayout, emptyLayout };
}));
