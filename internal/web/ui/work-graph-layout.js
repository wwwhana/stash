(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root) root.StashWorkGraph = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const ROOT_CARD_WIDTH = 216;
    const ROOT_CARD_BASE_HEIGHT = 92;
    const ROOT_CARD_CHILD_TOGGLE_HEIGHT = 34;
    const ROOT_STAGE_MIN_WIDTH = 224;
    const ROOT_STAGE_GAP = 24;
    const ROOT_ROW_GAP = 12;
    const ROOT_PADDING_X = 2;
    const ROOT_STAGE_TOP = 12;
    const ROOT_STAGE_HEADER_HEIGHT = 42;
    const ROOT_PADDING_BOTTOM = 12;
    const ROOT_MIN_HEIGHT = 200;

    const CHILD_NODE_WIDTH = 138;
    const CHILD_NODE_HEIGHT = 62;
    const CHILD_STAGE_WIDTH = 146;
    const CHILD_STAGE_GAP = 14;
    const CHILD_ROW_GAP = 8;
    const CHILD_PADDING_X = 2;
    const CHILD_STAGE_TOP = 10;
    const CHILD_STAGE_HEADER_HEIGHT = 28;
    const CHILD_PADDING_BOTTOM = 8;
    const CHILD_MIN_WIDTH = 150;

    function keyOf(value) {
        return String(value);
    }

    function nodeName(node) {
        return String(node && (node.issue_key || node.title || ('#' + node.id)) || '작업');
    }

    function dragKey(scope, key) {
        return `${scope}:${key}`;
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

    function compareNodes(left, right) {
        const byName = nodeName(left).localeCompare(nodeName(right), 'ko');
        if (byName !== 0) return byName;
        return keyOf(left && left.id).localeCompare(keyOf(right && right.id));
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
            fromKey: keyOf(edge && edge.from_item_id),
            toKey: keyOf(edge && edge.to_item_id),
            type: String(edge && edge.edge_type || 'relates_to')
        })).filter(edge => nodeMap.has(edge.fromKey) && nodeMap.has(edge.toKey));
    }

    function buildHierarchy(nodes, nodeMap) {
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

        for (const node of nodes) {
            const start = keyOf(node.id);
            const chain = [];
            const localIndex = new Map();
            let current = start;
            while (parentByNode.has(current)) {
                if (localIndex.has(current)) {
                    const cycleKeys = chain.slice(localIndex.get(current));
                    cycleKeys.forEach(key => hierarchyCycleNodes.add(key));
                    const breakKey = cycleKeys.slice().sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)))[0];
                    parentByNode.delete(breakKey);
                    break;
                }
                localIndex.set(current, chain.length);
                chain.push(current);
                current = parentByNode.get(current);
            }
        }

        const childrenByParent = new Map(nodes.map(node => [keyOf(node.id), []]));
        for (const [childKey, parentKey] of parentByNode) childrenByParent.get(parentKey).push(childKey);
        for (const children of childrenByParent.values()) {
            children.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));
        }

        const rootByNode = new Map();
        const depthByNode = new Map();
        for (const node of nodes) {
            const key = keyOf(node.id);
            let current = key;
            let depth = 0;
            const guard = new Set();
            while (parentByNode.has(current) && !guard.has(current)) {
                guard.add(current);
                current = parentByNode.get(current);
                depth += 1;
            }
            rootByNode.set(key, current);
            depthByNode.set(key, depth);
        }

        const descendantsByRoot = new Map();
        for (const node of nodes) {
            const key = keyOf(node.id);
            const rootKey = rootByNode.get(key);
            if (!descendantsByRoot.has(rootKey)) descendantsByRoot.set(rootKey, []);
            if (key !== rootKey) descendantsByRoot.get(rootKey).push(key);
        }
        for (const descendants of descendantsByRoot.values()) {
            descendants.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));
        }

        return {
            parentByNode,
            orphanParentByNode,
            hierarchyCycleNodes,
            childrenByParent,
            rootByNode,
            depthByNode,
            descendantsByRoot
        };
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

    function computeLevels(nodeKeys, graphEdges, nodeMap) {
        if (!nodeKeys.length) {
            return { stages: [], cycles: [], cycleByNode: new Map(), maxDepth: -1 };
        }
        const keySet = new Set(nodeKeys);
        const outgoing = new Map(nodeKeys.map(key => [key, []]));
        const reverseOutgoing = new Map(nodeKeys.map(key => [key, []]));
        const selfLoops = new Set();
        const blockEdges = graphEdges.filter(edge => edge.type === 'blocks' && keySet.has(edge.fromKey) && keySet.has(edge.toKey));
        for (const edge of blockEdges) {
            outgoing.get(edge.fromKey).push(edge.toKey);
            reverseOutgoing.get(edge.toKey).push(edge.fromKey);
            if (edge.fromKey === edge.toKey) selfLoops.add(edge.fromKey);
        }

        const components = stronglyConnectedComponents(nodeKeys, outgoing, reverseOutgoing);
        for (const component of components) {
            component.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));
        }
        const componentByNode = new Map();
        components.forEach((component, componentIndex) => {
            component.forEach(key => componentByNode.set(key, componentIndex));
        });

        const componentOutgoing = components.map(() => new Set());
        const componentIncoming = components.map(() => new Set());
        for (const edge of blockEdges) {
            const fromComponent = componentByNode.get(edge.fromKey);
            const toComponent = componentByNode.get(edge.toKey);
            if (fromComponent === toComponent) continue;
            componentOutgoing[fromComponent].add(toComponent);
            componentIncoming[toComponent].add(fromComponent);
        }

        const componentName = componentIndex => nodeName(nodeMap.get(components[componentIndex][0]));
        const queue = [];
        const indegree = componentIncoming.map(incoming => incoming.size);
        const depth = components.map(() => 0);
        const enqueue = componentIndex => {
            queue.push(componentIndex);
            queue.sort((left, right) => componentName(left).localeCompare(componentName(right), 'ko'));
        };
        indegree.forEach((value, componentIndex) => {
            if (value === 0) enqueue(componentIndex);
        });
        while (queue.length) {
            const componentIndex = queue.shift();
            for (const successor of componentOutgoing[componentIndex]) {
                depth[successor] = Math.max(depth[successor], depth[componentIndex] + 1);
                indegree[successor] -= 1;
                if (indegree[successor] === 0) enqueue(successor);
            }
        }

        const cycles = [];
        const cycleByNode = new Map();
        components.forEach((component, componentIndex) => {
            if (component.length === 1 && !selfLoops.has(component[0])) return;
            const cycle = {
                id: `cycle-${cycles.length + 1}`,
                nodeIds: component.map(key => nodeMap.get(key).id),
                nodeKeys: component.slice(),
                label: component.map(key => nodeName(nodeMap.get(key))).join(' ↔ ')
            };
            cycles.push(cycle);
            component.forEach(key => cycleByNode.set(key, cycle));
        });

        const maxDepth = Math.max(...depth, 0);
        const stages = Array.from({ length: maxDepth + 1 }, () => []);
        components.forEach((component, componentIndex) => {
            component.forEach(key => stages[depth[componentIndex]].push(key));
        });
        stages.forEach(members => members.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right))));
        return { stages, cycles, cycleByNode, maxDepth };
    }

    function edgePath(source, target, index) {
        const deltaX = target.x - source.x;
        const sourceY = source.anchorY === undefined ? source.y : source.anchorY;
        const targetY = target.anchorY === undefined ? target.y : target.anchorY;
        const sourceEdgeHeight = source.edgeHeight || source.height;
        const targetEdgeHeight = target.edgeHeight || target.height;
        if (Math.abs(deltaX) < 16) {
            const yDirection = targetY >= sourceY ? 1 : -1;
            const sourceX = source.x + source.width / 2;
            const targetX = target.x + target.width / 2;
            const sourceEdgeY = sourceY + yDirection * Math.min(18, sourceEdgeHeight / 4);
            const targetEdgeY = targetY - yDirection * Math.min(18, targetEdgeHeight / 4);
            const bend = 48 + (index % 4) * 14;
            return `M ${sourceX} ${sourceEdgeY} C ${sourceX + bend} ${sourceEdgeY}, ${targetX + bend} ${targetEdgeY}, ${targetX} ${targetEdgeY}`;
        }
        const direction = deltaX > 0 ? 1 : -1;
        const sourceX = source.x + direction * source.width / 2;
        const targetX = target.x - direction * target.width / 2;
        const control = Math.max(16, Math.abs(targetX - sourceX) * 0.44);
        return `M ${sourceX} ${sourceY} C ${sourceX + direction * control} ${sourceY}, ${targetX - direction * control} ${targetY}, ${targetX} ${targetY}`;
    }

    function stageLabel(depth) {
        return depth === 0 ? '시작' : `${depth + 1}단계`;
    }

    function edgeKey(entry, prefix) {
        if (entry.edge && entry.edge.id !== undefined && entry.edge.id !== null) return `${prefix}-${entry.edge.id}`;
        return `${prefix}-${entry.fromKey}-${entry.toKey}-${entry.type}-${entry.index}`;
    }

    function decorateLayoutEdges(graphEdges, layoutByNode, cycleByNode, prefix) {
        return graphEdges.filter(edge => layoutByNode.has(edge.fromKey) && layoutByNode.has(edge.toKey)).map((entry, index) => {
            const source = layoutByNode.get(entry.fromKey);
            const target = layoutByNode.get(entry.toKey);
            const sourceCycle = cycleByNode.get(entry.fromKey);
            const cycle = entry.type === 'blocks' && sourceCycle && sourceCycle === cycleByNode.get(entry.toKey);
            const fromName = nodeName(source.item);
            const toName = nodeName(target.item);
            return {
                key: edgeKey(entry, prefix),
                edge: entry.edge,
                rawEdges: entry.rawEdges || [entry.edge],
                type: entry.type,
                cycle: Boolean(cycle),
                path: edgePath(source, target, index),
                stroke: cycle ? '#fb7185' : (entry.type === 'blocks' ? '#818cf8' : '#64748b'),
                dashArray: entry.type === 'blocks' ? null : '7 6',
                marker: entry.type === 'blocks',
                ariaLabel: entry.ariaLabel || (entry.type === 'blocks'
                    ? `${fromName}가 끝나야 ${toName}를 진행할 수 있습니다.`
                    : `${fromName}과 ${toName}는 관련된 작업입니다.`)
            };
        });
    }

    function childEntry(key, hierarchy, nodeMap, offsets) {
        const parentKey = hierarchy.parentByNode.get(key);
        const keyForDrag = dragKey('child', key);
        return {
            key,
            item: nodeMap.get(key),
            dragKey: keyForDrag,
            offset: nodeOffset(offsets, keyForDrag),
            hierarchyDepth: hierarchy.depthByNode.get(key) || 1,
            parentItem: parentKey ? nodeMap.get(parentKey) : null,
            hierarchyCycle: hierarchy.hierarchyCycleNodes.has(key)
        };
    }

    function buildChildLayout(rootKey, descendantKeys, edges, hierarchy, nodeMap, offsets) {
        if (!descendantKeys.length) {
            return {
                width: 0, height: 0, canvasStyle: '', stages: [], nodes: [], edges: [],
                disconnected: [], cycles: [], flowHeight: 0, detachedStyle: ''
            };
        }
        const descendantSet = new Set(descendantKeys);
        const childEdges = edges.filter(edge => descendantSet.has(edge.fromKey) && descendantSet.has(edge.toKey));
        const incident = new Set();
        childEdges.forEach(edge => {
            incident.add(edge.fromKey);
            incident.add(edge.toKey);
        });
        const flowKeys = descendantKeys.filter(key => incident.has(key));
        const disconnectedKeys = descendantKeys.filter(key => !incident.has(key));
        const levelData = computeLevels(flowKeys, childEdges, nodeMap);

        const flowWidth = levelData.stages.length
            ? CHILD_PADDING_X * 2 + levelData.stages.length * CHILD_STAGE_WIDTH + Math.max(0, levelData.stages.length - 1) * CHILD_STAGE_GAP
            : 0;
        const maxRows = Math.max(0, ...levelData.stages.map(members => members.length));
        const flowHeight = levelData.stages.length
            ? CHILD_STAGE_TOP + CHILD_STAGE_HEADER_HEIGHT + maxRows * CHILD_NODE_HEIGHT + Math.max(0, maxRows - 1) * CHILD_ROW_GAP + CHILD_PADDING_BOTTOM
            : 0;
        const baseWidth = Math.max(CHILD_MIN_WIDTH, flowWidth);
        const detachedColumns = baseWidth >= 560 ? 3 : (baseWidth >= 360 ? 2 : 1);
        const detachedRows = Math.ceil(disconnectedKeys.length / detachedColumns);
        const detachedHeight = disconnectedKeys.length ? 45 + detachedRows * 54 + Math.max(0, detachedRows - 1) * 7 + 10 : 0;
        const detachedTop = flowHeight ? flowHeight + 10 : 0;
        const baseHeight = Math.max(1, detachedTop + detachedHeight);
        const childOffsets = descendantKeys.map(key => nodeOffset(offsets, dragKey('child', key)));
        const width = baseWidth + Math.max(0, ...childOffsets.map(offset => offset.x));
        const height = baseHeight + Math.max(0, ...childOffsets.map(offset => offset.y));
        const stageHeight = Math.max(1, flowHeight - CHILD_STAGE_TOP - CHILD_PADDING_BOTTOM);
        const layoutByNode = new Map();
        const stages = levelData.stages.map((members, depth) => {
            const left = CHILD_PADDING_X + depth * (CHILD_STAGE_WIDTH + CHILD_STAGE_GAP);
            const stage = {
                depth,
                label: stageLabel(depth),
                style: `left: ${left}px; top: ${CHILD_STAGE_TOP}px; width: ${CHILD_STAGE_WIDTH}px; height: ${stageHeight}px`,
                nodes: []
            };
            members.forEach((key, row) => {
                const base = childEntry(key, hierarchy, nodeMap, offsets);
                const baseX = left + CHILD_STAGE_WIDTH / 2;
                const baseY = CHILD_STAGE_TOP + CHILD_STAGE_HEADER_HEIGHT + CHILD_NODE_HEIGHT / 2 + row * (CHILD_NODE_HEIGHT + CHILD_ROW_GAP);
                const x = baseX + base.offset.x;
                const y = baseY + base.offset.y;
                const layoutNode = {
                    ...base,
                    baseX,
                    baseY,
                    x,
                    y,
                    width: CHILD_NODE_WIDTH,
                    height: CHILD_NODE_HEIGHT,
                    canvasWidth: width,
                    canvasHeight: height,
                    depth,
                    stageLabel: stage.label,
                    cycle: levelData.cycleByNode.get(key) || null,
                    style: `left: ${x - CHILD_NODE_WIDTH / 2}px; top: ${y - CHILD_NODE_HEIGHT / 2}px`
                };
                stage.nodes.push(layoutNode);
                layoutByNode.set(key, layoutNode);
            });
            return stage;
        });
        const layoutEdges = decorateLayoutEdges(childEdges, layoutByNode, levelData.cycleByNode, `child-${rootKey}`);
        const disconnected = disconnectedKeys.map(key => childEntry(key, hierarchy, nodeMap, offsets));
        return {
            width,
            height,
            canvasStyle: `width: ${width}px; height: ${height}px`,
            stages,
            nodes: stages.flatMap(stage => stage.nodes),
            edges: layoutEdges,
            disconnected,
            cycles: levelData.cycles,
            flowHeight,
            detachedStyle: `top: ${detachedTop}px; width: ${baseWidth}px; --child-columns: ${detachedColumns}`
        };
    }

    function aggregateRootEdges(edges, hierarchy, nodeMap) {
        const aggregated = new Map();
        const crossParentLinks = [];
        for (const entry of edges) {
            const fromRoot = hierarchy.rootByNode.get(entry.fromKey);
            const toRoot = hierarchy.rootByNode.get(entry.toKey);
            if (fromRoot === toRoot) continue;
            const aggregationKey = `${fromRoot}\u0000${toRoot}\u0000${entry.type}`;
            if (!aggregated.has(aggregationKey)) {
                aggregated.set(aggregationKey, {
                    edge: null,
                    index: aggregated.size,
                    fromKey: fromRoot,
                    toKey: toRoot,
                    type: entry.type,
                    rawEdges: []
                });
            }
            const grouped = aggregated.get(aggregationKey);
            grouped.rawEdges.push(entry.edge);
            if (!grouped.edge) grouped.edge = entry.edge;
            if (entry.fromKey !== fromRoot || entry.toKey !== toRoot) {
                crossParentLinks.push({
                    key: edgeKey(entry, 'cross-parent'),
                    type: entry.type,
                    fromRoot: nodeMap.get(fromRoot),
                    toRoot: nodeMap.get(toRoot),
                    fromItem: nodeMap.get(entry.fromKey),
                    toItem: nodeMap.get(entry.toKey),
                    label: entry.type === 'blocks'
                        ? `${nodeName(nodeMap.get(entry.fromKey))} → ${nodeName(nodeMap.get(entry.toKey))}`
                        : `${nodeName(nodeMap.get(entry.fromKey))} · ${nodeName(nodeMap.get(entry.toKey))}`
                });
            }
        }
        for (const entry of aggregated.values()) {
            const cross = crossParentLinks.find(link => keyOf(link.fromRoot.id) === entry.fromKey && keyOf(link.toRoot.id) === entry.toKey && link.type === entry.type);
            if (cross) {
                entry.ariaLabel = entry.type === 'blocks'
                    ? `${nodeName(cross.fromRoot)}의 ${nodeName(cross.fromItem)}가 끝나야 ${nodeName(cross.toRoot)}의 ${nodeName(cross.toItem)}를 진행할 수 있습니다.`
                    : `${nodeName(cross.fromRoot)}의 ${nodeName(cross.fromItem)}와 ${nodeName(cross.toRoot)}의 ${nodeName(cross.toItem)}는 관련된 작업입니다.`;
            }
        }
        return { edges: Array.from(aggregated.values()), crossParentLinks };
    }

    function emptyLayout() {
        return {
            width: 0,
            height: 0,
            canvasStyle: '',
            stages: [],
            nodes: [],
            edges: [],
            disconnected: [],
            cycles: [],
            crossParentLinks: [],
            hierarchyWarnings: [],
            maxDepth: -1,
            sourceNodeCount: 0
        };
    }

    function buildWorkGraphLayout(rawNodes, rawEdges, options) {
        const nodes = normalizeNodes(rawNodes);
        const nodeMap = new Map(nodes.map(node => [keyOf(node.id), node]));
        const edges = normalizeEdges(rawEdges, nodeMap);
        const hierarchy = buildHierarchy(nodes, nodeMap);
        const expandedIds = new Set(Array.isArray(options && options.expandedIds) ? options.expandedIds.map(keyOf) : []);
        const offsets = options && options.offsets && typeof options.offsets === 'object' ? options.offsets : {};
        const rootKeys = nodes.map(node => keyOf(node.id)).filter(key => hierarchy.rootByNode.get(key) === key);
        rootKeys.sort((left, right) => compareNodes(nodeMap.get(left), nodeMap.get(right)));

        const incidentRoots = new Set();
        edges.forEach(edge => {
            incidentRoots.add(hierarchy.rootByNode.get(edge.fromKey));
            incidentRoots.add(hierarchy.rootByNode.get(edge.toKey));
        });
        const connectedRootKeys = [];
        const disconnectedRootKeys = [];
        for (const rootKey of rootKeys) {
            const descendants = hierarchy.descendantsByRoot.get(rootKey) || [];
            if (descendants.length || incidentRoots.has(rootKey)) connectedRootKeys.push(rootKey);
            else disconnectedRootKeys.push(rootKey);
        }

        const aggregated = aggregateRootEdges(edges, hierarchy, nodeMap);
        const levelData = computeLevels(connectedRootKeys, aggregated.edges, nodeMap);
        const rootMeta = new Map();
        const childCycles = [];
        for (const rootKey of connectedRootKeys) {
            const descendants = hierarchy.descendantsByRoot.get(rootKey) || [];
            const childLayout = buildChildLayout(rootKey, descendants, edges, hierarchy, nodeMap, offsets);
            childLayout.cycles.forEach(cycle => childCycles.push({
                ...cycle,
                id: `child-${rootKey}-${cycle.id}`,
                label: `${nodeName(nodeMap.get(rootKey))}: ${cycle.label}`
            }));
            const expanded = descendants.length > 0 && expandedIds.has(rootKey);
            const orphanParentId = hierarchy.orphanParentByNode.get(rootKey);
            const hierarchyCycle = hierarchy.hierarchyCycleNodes.has(rootKey);
            const warningHeight = orphanParentId !== undefined || hierarchyCycle ? 24 : 0;
            const baseHeight = ROOT_CARD_BASE_HEIGHT + warningHeight + (descendants.length ? ROOT_CARD_CHILD_TOGGLE_HEIGHT : 0);
            const width = expanded ? Math.max(ROOT_CARD_WIDTH, childLayout.width + 12) : ROOT_CARD_WIDTH;
            const height = expanded ? baseHeight + childLayout.height + 8 : baseHeight;
            rootMeta.set(rootKey, {
                descendants: descendants.map(key => nodeMap.get(key)),
                directChildren: (hierarchy.childrenByParent.get(rootKey) || []).map(key => nodeMap.get(key)),
                childLayout,
                expanded,
                orphanParentId,
                hierarchyCycle,
                width,
                height,
                baseHeight
            });
        }

        const stageWidths = levelData.stages.map(members => Math.max(
            ROOT_STAGE_MIN_WIDTH,
            ...members.map(key => rootMeta.get(key).width + 8)
        ));
        const stageLefts = [];
        let nextLeft = ROOT_PADDING_X;
        stageWidths.forEach(width => {
            stageLefts.push(nextLeft);
            nextLeft += width + ROOT_STAGE_GAP;
        });
        const baseWidth = levelData.stages.length ? nextLeft - ROOT_STAGE_GAP + ROOT_PADDING_X : 0;
        const stageContentHeights = levelData.stages.map(members => members.reduce((sum, key, index) => (
            sum + rootMeta.get(key).height + (index ? ROOT_ROW_GAP : 0)
        ), 0));
        const baseHeight = levelData.stages.length ? Math.max(
            ROOT_MIN_HEIGHT,
            ROOT_STAGE_TOP + ROOT_STAGE_HEADER_HEIGHT + Math.max(...stageContentHeights, 0) + ROOT_PADDING_BOTTOM
        ) : 0;
        const rootOffsets = connectedRootKeys.map(key => nodeOffset(offsets, dragKey('root', key)));
        const width = baseWidth + Math.max(0, ...rootOffsets.map(offset => offset.x));
        const height = baseHeight + Math.max(0, ...rootOffsets.map(offset => offset.y));
        const stageHeight = Math.max(1, height - ROOT_STAGE_TOP - ROOT_PADDING_BOTTOM);
        const layoutByNode = new Map();
        const stages = levelData.stages.map((members, depth) => {
            const left = stageLefts[depth];
            const stageWidth = stageWidths[depth];
            const stage = {
                depth,
                label: stageLabel(depth),
                style: `left: ${left}px; top: ${ROOT_STAGE_TOP}px; width: ${stageWidth}px; height: ${stageHeight}px`,
                nodes: []
            };
            let top = ROOT_STAGE_TOP + ROOT_STAGE_HEADER_HEIGHT;
            members.forEach(key => {
                const item = nodeMap.get(key);
                const meta = rootMeta.get(key);
                const keyForDrag = dragKey('root', key);
                const offset = nodeOffset(offsets, keyForDrag);
                const baseX = left + stageWidth / 2;
                const baseY = top + meta.height / 2;
                const x = baseX + offset.x;
                const y = baseY + offset.y;
                const layoutNode = {
                    key,
                    item,
                    ...meta,
                    dragKey: keyForDrag,
                    offset,
                    baseX,
                    baseY,
                    x,
                    y,
                    canvasWidth: width,
                    canvasHeight: height,
                    width: meta.width,
                    height: meta.height,
                    anchorY: top + ROOT_CARD_BASE_HEIGHT / 2 + offset.y,
                    edgeHeight: ROOT_CARD_BASE_HEIGHT,
                    depth,
                    stageLabel: stage.label,
                    cycle: levelData.cycleByNode.get(key) || null,
                    style: `left: ${x - meta.width / 2}px; top: ${y - meta.height / 2}px; width: ${meta.width}px; height: ${meta.height}px`
                };
                stage.nodes.push(layoutNode);
                layoutByNode.set(key, layoutNode);
                top += meta.height + ROOT_ROW_GAP;
            });
            return stage;
        });

        const layoutEdges = decorateLayoutEdges(aggregated.edges, layoutByNode, levelData.cycleByNode, 'root');
        const disconnected = disconnectedRootKeys.map(key => ({
            key,
            item: nodeMap.get(key),
            dragKey: dragKey('root', key),
            offset: nodeOffset(offsets, dragKey('root', key)),
            orphanParentId: hierarchy.orphanParentByNode.get(key),
            hierarchyCycle: hierarchy.hierarchyCycleNodes.has(key)
        }));
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
            canvasStyle: width && height ? `width: ${width}px; height: ${height}px` : '',
            stages,
            nodes: stages.flatMap(stage => stage.nodes),
            edges: layoutEdges,
            disconnected,
            cycles: [
                ...levelData.cycles.map(cycle => ({ ...cycle, id: `root-${cycle.id}` })),
                ...childCycles
            ],
            crossParentLinks: aggregated.crossParentLinks,
            hierarchyWarnings,
            maxDepth: levelData.maxDepth,
            sourceNodeCount: nodes.length
        };
    }

    return {
        buildWorkGraphLayout,
        emptyLayout
    };
}));
