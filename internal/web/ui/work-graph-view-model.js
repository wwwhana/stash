(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./work-graph-layout.js'), require('./search-utils.js'))
        : factory(root.StashWorkGraph, root.StashSearch);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashWorkGraphViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (workGraphLayout, searchUtils) {
    'use strict';

    if (!workGraphLayout) throw new Error('작업 흐름 배치 모듈을 불러오지 못했습니다.');
    if (!searchUtils) throw new Error('검색 모듈을 불러오지 못했습니다.');

    function escapeSVGAttribute(value) {
        return String(value === undefined || value === null ? '' : value)
            .replace(/&/g, '&amp;')
            .replace(/"/g, '&quot;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;');
    }

    function createWorkGraphViewModel() {
        const layout = workGraphLayout;
        const defaultRelations = () => ({ part_of: true, blocks: true, relates_to: true });
        const emptyGraphPage = () => ({ hasMore: false, nextNodeOffset: 0, nextEdgeOffset: 0, totalNodes: 0, totalEdges: 0 });
        const relationGroup = type => type === 'part_of' || type === 'blocks' ? type : 'relates_to';
        let activeDragHandle = null;
        let graphFilterTrigger = null;
        let graphFocusReturn = null;
        let graphPageRequestSequence = 0;
        let graphSearchRequestSequence = 0;
        return {
            graph: { nodes: [], edges: [], worktrees: [] },
            graphPage: emptyGraphPage(),
            graphPageLoading: false,
            graphPageError: '',
            graphFocusLoading: false,
            graphFocusError: '',
            workGraphLayout: layout.emptyLayout(),
            graphFilter: { query: '', status: '', agent: '', relations: defaultRelations() },
            graphFilterOpen: false,
            graphFocusedKey: '',
            graphInspectorOverlay: false,
            graphNodeOffsets: {},
            graphDragState: null,
            graphMoveAnnouncement: '',
            graphError: '',

            setWorkGraph(value) {
                const graph = value && typeof value === 'object' ? value : {};
                this.graph = {
                    nodes: Array.isArray(graph.nodes) ? graph.nodes : [],
                    edges: Array.isArray(graph.edges) ? graph.edges : [],
                    worktrees: Array.isArray(graph.worktrees) ? graph.worktrees : []
                };
                this.graphPage = {
                    hasMore: Boolean(graph.has_more),
                    nextNodeOffset: Number(graph.next_node_offset) || 0,
                    nextEdgeOffset: Number(graph.next_edge_offset) || 0,
                    totalNodes: Number(graph.total_nodes) || this.graph.nodes.length,
                    totalEdges: Number(graph.total_edges) || this.graph.edges.length
                };
                this.graphPageError = '';
                const validIDs = new Set(this.graph.nodes.map(item => String(item.id)));
                const validOffsetKeys = new Set(Array.from(validIDs).map(id => `node:${id}`));
                this.graphNodeOffsets = Object.fromEntries(
                    Object.entries(this.graphNodeOffsets).filter(([key, offset]) => (
                        validOffsetKeys.has(key) && offset && (Number(offset.x) || Number(offset.y))
                    ))
                );
                if (this.graph.nodes.length && this.graphFocusedKey && !validIDs.has(String(this.graphFocusedKey)) && !this.graphPage.hasMore) {
                    this.graphFocusedKey = '';
                    if (typeof this.clearWorkMonitor === 'function') this.clearWorkMonitor();
                }
                this.refreshWorkGraphLayout();
            },

            clearWorkGraph() {
                graphPageRequestSequence += 1;
                graphSearchRequestSequence += 1;
                this.graphPageLoading = false;
                this.setWorkGraph({ nodes: [], edges: [], worktrees: [] });
            },

            appendWorkGraphPage(value) {
                const page = value && typeof value === 'object' ? value : {};
                const merge = (current, incoming, keyOfItem) => {
                    const result = current.slice();
                    const seen = new Set(result.map(keyOfItem));
                    for (const item of Array.isArray(incoming) ? incoming : []) {
                        const key = keyOfItem(item);
                        if (seen.has(key)) continue;
                        seen.add(key);
                        result.push(item);
                    }
                    return result;
                };
                const nodes = merge(this.graph.nodes, page.nodes, item => String(item && item.id));
                const edges = merge(this.graph.edges, page.edges, item => item && item.id !== undefined && item.id !== null
                    ? `id:${item.id}`
                    : `${item && item.from_item_id}:${item && item.to_item_id}:${item && item.edge_type}`);
                const worktrees = merge(this.graph.worktrees, page.worktrees, item => String(item && item.id));
                this.setWorkGraph({ ...page, nodes, edges, worktrees });
            },

            async loadMoreWorkGraph() {
                if (!this.graphPage.hasMore || this.graphPageLoading) return false;
                const requestSequence = ++graphPageRequestSequence;
                const before = {
                    nodes: this.graph.nodes.length,
                    edges: this.graph.edges.length,
                    nodeOffset: this.graphPage.nextNodeOffset,
                    edgeOffset: this.graphPage.nextEdgeOffset
                };
                this.graphPageLoading = true;
                this.graphPageError = '';
                try {
                    const args = {
                        include_done: true,
                        node_offset: this.graphPage.nextNodeOffset,
                        edge_offset: this.graphPage.nextEdgeOffset
                    };
                    const selectedScope = this.mapNamespaceSlug || this.graphProjectSlug;
                    if (selectedScope) args.project = selectedScope;
                    else args.namespaces = '/';
                    const data = await this.invokeTool('get_work_graph', args);
                    if (requestSequence !== graphPageRequestSequence) return false;
                    const page = this.toolValue(data);
                    if (!page || typeof page !== 'object') throw new Error('작업 흐름 응답이 올바르지 않습니다.');
                    this.appendWorkGraphPage(page);
                    this.resultValue = this.graph;
                    this.resultKind = 'get_work_graph';
                    this.result = JSON.stringify(this.graph, null, 2);
                    if (typeof this.markLoaded === 'function') this.markLoaded();
                    return this.graph.nodes.length > before.nodes || this.graph.edges.length > before.edges ||
                        this.graphPage.nextNodeOffset > before.nodeOffset || this.graphPage.nextEdgeOffset > before.edgeOffset ||
                        !this.graphPage.hasMore;
                } catch (error) {
                    if (requestSequence !== graphPageRequestSequence) return false;
                    this.graphPageError = String(error && error.message || '').trim() || '다음 작업을 불러오지 못했습니다.';
                    return false;
                } finally {
                    if (requestSequence === graphPageRequestSequence) this.graphPageLoading = false;
                }
            },

            async loadAllWorkGraphForSearch(maxPages = 256) {
                if (!String(this.graphFilter.query || '').trim() || this.loading || this.graphPageLoading) return false;
                const requestSequence = ++graphSearchRequestSequence;
                let pages = 0;
                while (this.graphPage.hasMore && pages < maxPages && requestSequence === graphSearchRequestSequence) {
                    pages += 1;
                    if (!await this.loadMoreWorkGraph()) break;
                }
                if (requestSequence !== graphSearchRequestSequence) return false;
                this.refreshWorkGraphLayout();
                return !this.graphPage.hasMore;
            },

            setWorkGraphWorktrees(worktrees) {
                this.graph = { ...this.graph, worktrees: Array.isArray(worktrees) ? worktrees : [] };
            },

            replaceWorkGraphNode(item) {
                if (!item || item.id === undefined || item.id === null) return;
                const index = this.graph.nodes.findIndex(candidate => Number(candidate.id) === Number(item.id));
                if (index < 0) return;
                this.graph.nodes.splice(index, 1, item);
                this.refreshWorkGraphLayout();
            },

            refreshWorkGraphLayout() {
                const query = String(this.graphFilter.query || '').trim();
                const status = String(this.graphFilter.status || '').trim();
                const agent = String(this.graphFilter.agent || '').trim();
                const hasNodeFilters = Boolean(query || status || agent);
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                const relationIsVisible = edge => relations[relationGroup(String(edge && edge.edge_type || 'relates_to'))] !== false;
                const byID = new Map(this.graph.nodes.map(item => [String(item.id), item]));
                const matchedIDs = new Set(this.graph.nodes.filter(item => {
                    if (status && item.status !== status) return false;
                    if (agent && String(item.agent_id || item.owner || '').trim() !== agent) return false;
                    if (!query) return true;
                    const values = [
                        item.issue_key, item.title, item.description, item.status, item.owner,
                        item.agent_id, item.reporter, item.issue_type, item.due_at,
                        item.latest_result, item.next_action, item.labels, item.required_capabilities
                    ];
                    return searchUtils.matchesSearch(values, query);
                }).map(item => String(item.id)));
                const visibleIDs = new Set(matchedIDs);
                if (this.graphFocusedKey && byID.has(String(this.graphFocusedKey))) visibleIDs.add(String(this.graphFocusedKey));
                if (hasNodeFilters) {
                    const addAncestors = startID => {
                        let current = byID.get(String(startID));
                        const guard = new Set();
                        while (current && current.parent_id !== undefined && current.parent_id !== null) {
                            const parentID = String(current.parent_id);
                            if (guard.has(parentID)) break;
                            guard.add(parentID);
                            visibleIDs.add(parentID);
                            current = byID.get(parentID);
                        }
                    };
                    for (const id of matchedIDs) {
                        addAncestors(id);
                    }
                    if (this.graphFocusedKey) addAncestors(this.graphFocusedKey);
                    for (const edge of this.graph.edges.filter(relationIsVisible)) {
                        const from = String(edge.from_item_id);
                        const to = String(edge.to_item_id);
                        if (matchedIDs.has(from)) visibleIDs.add(to);
                        if (matchedIDs.has(to)) visibleIDs.add(from);
                    }
                }
                const nodes = this.graph.nodes.filter(item => visibleIDs.has(String(item.id))).map(item => (
                    matchedIDs.has(String(item.id)) ? item : { ...item, __filter_context: true }
                ));
                const edges = this.graph.edges.filter(edge => relationIsVisible(edge) && (
                    visibleIDs.has(String(edge.from_item_id)) && visibleIDs.has(String(edge.to_item_id))
                ));
                this.workGraphLayout = layout.buildWorkGraphLayout(nodes, edges, {
                    offsets: this.graphNodeOffsets,
                    sourceNodeCount: this.graph.nodes.length,
                    relations
                });
                if (typeof this.updateGraphViewport === 'function') {
                    this.updateGraphViewport(this.workGraphLayout);
                }
                if (!this.loading && this.view === 'graph') this.syncRoute(true);
            },

            graphNodeDragKey(node) {
                if (node && node.dragKey) return node.dragKey;
                return `node:${node && node.item ? node.item.id : ''}`;
            },

            graphNodeMoveBounds(node) {
                const baseX = Number(node && node.baseX);
                const baseY = Number(node && node.baseY);
                const width = Number(node && node.width);
                const height = Number(node && node.height);
                const canvasWidth = Number(node && node.canvasWidth);
                const canvasHeight = Number(node && node.canvasHeight);
                if (![baseX, baseY, width, height, canvasWidth, canvasHeight].every(Number.isFinite)) {
                    return { minX: 0, minY: 0 };
                }
                return {
                    minX: width / 2 - baseX,
                    minY: height / 2 - baseY,
                };
            },

            writeGraphNodeOffset(key, x, y, bounds = null) {
                const clamp = (value, minimum, maximum) => {
                    let result = Math.round(Number(value) || 0);
                    if (bounds && Number.isFinite(minimum)) result = Math.max(minimum, result);
                    if (bounds && Number.isFinite(maximum)) result = Math.min(maximum, result);
                    return result;
                };
                const nextOffset = {
                    x: clamp(x, bounds && bounds.minX, bounds && bounds.maxX),
                    y: clamp(y, bounds && bounds.minY, bounds && bounds.maxY)
                };
                const next = { ...this.graphNodeOffsets };
                if (nextOffset.x || nextOffset.y) next[key] = nextOffset;
                else delete next[key];
                this.graphNodeOffsets = next;
                this.refreshWorkGraphLayout();
            },

            startGraphNodeDrag(event, node) {
                if (!event || !node || (event.isPrimary === false) || (event.button !== undefined && event.button !== 0)) return;
                event.preventDefault();
                event.stopPropagation();
                const key = this.graphNodeDragKey(node);
                const origin = this.graphNodeOffsets[key] || { x: 0, y: 0 };
                this.graphDragState = {
                    key,
                    pointerId: event.pointerId,
                    startX: event.clientX,
                    startY: event.clientY,
                    originX: Number(origin.x) || 0,
                    originY: Number(origin.y) || 0,
                    bounds: this.graphNodeMoveBounds(node),
                    label: node.item.issue_key || node.item.title || '#' + node.item.id
                };
                activeDragHandle = event.currentTarget;
                if (activeDragHandle && activeDragHandle.setPointerCapture && event.pointerId !== undefined) {
                    try { activeDragHandle.setPointerCapture(event.pointerId); } catch (_) {}
                }
                if (activeDragHandle && activeDragHandle.focus) activeDragHandle.focus({ preventScroll: true });
                document.body.classList.add('stash-graph-dragging');
            },

            moveGraphNodeDrag(event) {
                const state = this.graphDragState;
                if (!state || !event || (state.pointerId !== undefined && event.pointerId !== state.pointerId)) return;
                if (event.cancelable) event.preventDefault();
                const scale = typeof this.graphViewportScale === 'function' ? this.graphViewportScale() : 1;
                this.writeGraphNodeOffset(
                    state.key,
                    state.originX + (event.clientX - state.startX) / scale,
                    state.originY + (event.clientY - state.startY) / scale,
                    state.bounds
                );
            },

            finishGraphNodeDrag(event) {
                const state = this.graphDragState;
                if (!state || (event && state.pointerId !== undefined && event.pointerId !== state.pointerId)) return;
                if (activeDragHandle && activeDragHandle.releasePointerCapture && state.pointerId !== undefined) {
                    try { activeDragHandle.releasePointerCapture(state.pointerId); } catch (_) {}
                }
                activeDragHandle = null;
                this.graphDragState = null;
                document.body.classList.remove('stash-graph-dragging');
                this.graphMoveAnnouncement = `${state.label} 위치를 옮겼습니다.`;
            },

            cancelGraphNodeDrag(event) {
                const state = this.graphDragState;
                if (!state) return;
                if (event) {
                    event.preventDefault();
                    event.stopPropagation();
                }
                this.writeGraphNodeOffset(state.key, state.originX, state.originY, state.bounds);
                if (activeDragHandle && activeDragHandle.releasePointerCapture && state.pointerId !== undefined) {
                    try { activeDragHandle.releasePointerCapture(state.pointerId); } catch (_) {}
                }
                activeDragHandle = null;
                this.graphDragState = null;
                document.body.classList.remove('stash-graph-dragging');
                this.graphMoveAnnouncement = `${state.label} 이동을 취소했습니다.`;
            },

            moveGraphNodeWithKeyboard(event, node) {
                const direction = {
                    ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1]
                }[event && event.key];
                if (!direction || !node) return;
                event.preventDefault();
                event.stopPropagation();
                const key = this.graphNodeDragKey(node);
                const current = this.graphNodeOffsets[key] || { x: 0, y: 0 };
                const step = event.shiftKey ? 40 : 8;
                this.writeGraphNodeOffset(
                    key,
                    (Number(current.x) || 0) + direction[0] * step,
                    (Number(current.y) || 0) + direction[1] * step,
                    this.graphNodeMoveBounds(node)
                );
                const label = node.item.issue_key || node.item.title || '#' + node.item.id;
                this.graphMoveAnnouncement = `${label} 위치를 ${step}픽셀 옮겼습니다.`;
            },

            graphDragHandleLabel(node) {
                const item = node && node.item || {};
                const label = item.issue_key || item.title || '#' + item.id;
                return `${label} 이동 손잡이. 방향키로 옮기고 Shift와 방향키로 크게 옮길 수 있습니다.`;
            },

            graphHasMovedNodes() {
                return Object.keys(this.graphNodeOffsets).length > 0;
            },

            resetGraphLayout() {
                if (!this.graphHasMovedNodes()) return;
                this.graphNodeOffsets = {};
                this.refreshWorkGraphLayout();
                this.graphMoveAnnouncement = '작업 위치를 기본 배치로 되돌렸습니다.';
            },

            toggleGraphFilterMenu(trigger = null) {
                if (this.graphFilterOpen) {
                    this.closeGraphFilterMenu(false);
                    return;
                }
                graphFilterTrigger = trigger || graphFilterTrigger;
                this.graphFilterOpen = true;
                const focusSearch = () => {
                    if (typeof document === 'undefined') return;
                    const search = document.getElementById('work-graph-filter-query');
                    if (search && typeof search.focus === 'function') search.focus({ preventScroll: true });
                };
                if (typeof this.$nextTick === 'function') this.$nextTick(focusSearch);
                else focusSearch();
                // x-show can apply its display change one task after Alpine's
                // reactive flush. Repeat once after that task so a real pointer
                // click always lands in the first filter field.
                if (typeof setTimeout === 'function') setTimeout(focusSearch, 0);
            },

            closeGraphFilterMenu(restoreFocus = false, restoreIfFocusInside = false) {
                const trigger = graphFilterTrigger;
                let shouldRestoreFocus = Boolean(restoreFocus);
                if (!shouldRestoreFocus && restoreIfFocusInside && typeof document !== 'undefined') {
                    const menu = document.getElementById('work-graph-filter-menu');
                    const active = document.activeElement;
                    shouldRestoreFocus = Boolean(
                        menu && active && typeof menu.contains === 'function' && menu.contains(active)
                    );
                }
                this.graphFilterOpen = false;
                graphFilterTrigger = null;
                if (!shouldRestoreFocus || !trigger || typeof trigger.focus !== 'function') return;
                const focusTrigger = () => {
                    if (trigger.isConnected === false) return;
                    trigger.focus({ preventScroll: true });
                };
                if (typeof this.$nextTick === 'function') this.$nextTick(focusTrigger);
                else if (typeof requestAnimationFrame === 'function') requestAnimationFrame(focusTrigger);
                else focusTrigger();
            },

            graphHasFilters() {
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                return Boolean(
                    String(this.graphFilter.query || '').trim() || String(this.graphFilter.status || '').trim() ||
                    String(this.graphFilter.agent || '').trim() ||
                    Object.values(relations).some(value => value === false)
                );
            },

            resetGraphFilters() {
                if (!this.graphHasFilters()) return;
                this.graphFilter = { query: '', status: '', agent: '', relations: defaultRelations() };
                this.refreshWorkGraphLayout();
            },

            toggleGraphRelation(relation) {
                if (!Object.prototype.hasOwnProperty.call(defaultRelations(), relation)) return;
                const current = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                this.graphFilter = {
                    ...this.graphFilter,
                    relations: { ...current, [relation]: !current[relation] }
                };
                this.refreshWorkGraphLayout();
            },

            graphFilterCount() {
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                return Number(Boolean(String(this.graphFilter.query || '').trim())) +
                    Number(Boolean(String(this.graphFilter.status || '').trim())) +
                    Number(Boolean(String(this.graphFilter.agent || '').trim())) +
                    Object.values(relations).filter(value => value === false).length;
            },

            graphFilterChips() {
                const chips = [];
                const query = String(this.graphFilter.query || '').trim();
                const status = String(this.graphFilter.status || '').trim();
                const agent = String(this.graphFilter.agent || '').trim();
                if (query) chips.push({ key: 'query', label: `검색: ${query}` });
                if (status) chips.push({ key: 'status', label: `상태: ${this.statusLabel(status)}` });
                if (agent) chips.push({ key: 'agent', label: `담당: ${agent}` });
                const labels = { part_of: '상위·하위', blocks: '먼저·다음', relates_to: '관련 관계' };
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                Object.keys(labels).filter(relation => relations[relation] === false).forEach(relation => {
                    chips.push({ key: `relation:${relation}`, label: `${labels[relation]} 숨김` });
                });
                return chips;
            },

            clearGraphFilter(key) {
                if (key === 'query') this.graphFilter.query = '';
                else if (key === 'status') this.graphFilter.status = '';
                else if (key === 'agent') this.graphFilter.agent = '';
                else if (String(key || '').startsWith('relation:')) {
                    const relation = String(key).slice('relation:'.length);
                    if (Object.prototype.hasOwnProperty.call(defaultRelations(), relation)) {
                        this.graphFilter = {
                            ...this.graphFilter,
                            relations: { ...defaultRelations(), ...(this.graphFilter.relations || {}), [relation]: true }
                        };
                    }
                }
                this.refreshWorkGraphLayout();
            },

            graphWorkLabel(item) {
                if (!item) return '';
                const key = item.issue_key || '#' + item.id;
                return item.title ? `${key} · ${item.title}` : key;
            },

            graphParentLabel(item) {
                const parent = this.graphParentFor(item);
                return parent ? `상위 ${this.graphWorkLabel(parent)}` : '';
            },

            graphFocusedItem() {
                if (!this.graphFocusedKey) return null;
                return this.graph.nodes.find(item => String(item.id) === String(this.graphFocusedKey)) || null;
            },

            graphParentFor(item) {
                if (!item || item.parent_id === undefined || item.parent_id === null) return null;
                return this.graph.nodes.find(candidate => String(candidate.id) === String(item.parent_id)) || null;
            },

            graphChildrenFor(item) {
                if (!item) return [];
                return this.graph.nodes.filter(candidate => (
                    candidate.parent_id !== undefined && candidate.parent_id !== null &&
                    String(candidate.parent_id) === String(item.id)
                )).sort((left, right) => this.graphWorkLabel(left).localeCompare(this.graphWorkLabel(right), 'ko'));
            },

            graphFocusedParent() {
                return this.graphParentFor(this.graphFocusedItem());
            },

            graphFocusedChildren() {
                return this.graphChildrenFor(this.graphFocusedItem());
            },

            graphFocusedPath() {
                const path = [];
                const visited = new Set();
                let item = this.graphFocusedItem();
                while (item && !visited.has(String(item.id))) {
                    visited.add(String(item.id));
                    path.unshift(item);
                    item = this.graphParentFor(item);
                }
                return path;
            },

            graphFocusedDependencyPath() {
                const focused = String(this.graphFocusedKey || '');
                const result = {
                    nodes: new Set(focused ? [focused] : []),
                    upstreamNodes: new Set(), downstreamNodes: new Set(),
                    upstreamEdges: new Set(), downstreamEdges: new Set()
                };
                if (!focused) return result;
                const blocking = this.graph.edges.filter(edge => String(edge && edge.edge_type || '') === 'blocks');
                const walk = (start, reverse, nodeSet, edgeSet) => {
                    const pending = [start];
                    const visited = new Set([start]);
                    while (pending.length) {
                        const current = pending.pop();
                        for (const edge of blocking) {
                            const from = String(edge.from_item_id);
                            const to = String(edge.to_item_id);
                            if ((reverse ? to : from) !== current) continue;
                            const next = reverse ? from : to;
                            edgeSet.add(edge.id === undefined || edge.id === null
                                ? `${from}:${to}:blocks`
                                : `work-edge-${edge.id}`);
                            nodeSet.add(next);
                            result.nodes.add(next);
                            if (!visited.has(next)) {
                                visited.add(next);
                                pending.push(next);
                            }
                        }
                    }
                };
                walk(focused, true, result.upstreamNodes, result.upstreamEdges);
                walk(focused, false, result.downstreamNodes, result.downstreamEdges);
                return result;
            },

            focusGraphNode(key, scroll = true, returnElement = null, focusInspector = false) {
                const normalized = String(key === undefined || key === null ? '' : key);
                if (!normalized || !this.graph.nodes.some(item => String(item.id) === normalized)) return;
                if (returnElement && typeof returnElement.focus === 'function') graphFocusReturn = returnElement;
                this.graphInspectorOverlay = this.graphInspectorIsOverlay();
                const changed = this.graphFocusedKey !== normalized;
                this.graphFocusedKey = normalized;
                this.graphFocusError = '';
                if (changed) this.syncRoute();
                this.refreshWorkGraphLayout();
                if (typeof this.loadWorkMonitor === 'function') this.loadWorkMonitor(Number(normalized));
                if (scroll) this.scrollGraphNodeIntoView(normalized);
                if (focusInspector) this.focusGraphInspector();
                this.graphMoveAnnouncement = `${this.graphWorkLabel(this.graphFocusedItem())} 선택`;
            },

            focusGraphNodeByID(id, focusInspector = false) {
                return this.focusGraphNodeWhenLoaded(id, focusInspector);
            },

            async focusGraphNodeWhenLoaded(id, focusInspector = false, maxPages = 64) {
                const key = String(id === undefined || id === null ? '' : id);
                if (!key) return false;
                this.graphFocusLoading = true;
                this.graphFocusError = '';
                let pageCount = 0;
                try {
                    while (!this.graph.nodes.some(item => String(item.id) === key) && this.graphPage.hasMore && pageCount < maxPages) {
                        pageCount += 1;
                        if (!await this.loadMoreWorkGraph()) break;
                    }
                    if (!this.graph.nodes.some(item => String(item.id) === key)) {
                        this.graphFocusError = this.graphPage.hasMore
                            ? '작업을 찾기 전에 불러오기 한도에 도달했습니다.'
                            : '해당 작업을 찾을 수 없습니다.';
                        return false;
                    }
                    this.focusGraphNode(key, true, null, focusInspector);
                    // A loaded parent ID is enough to move upward, but the only
                    // reliable way to discover every child is to finish the node
                    // pages. Do that only after the user explicitly selects work.
                    while (this.graphPage.hasMore && pageCount < maxPages) {
                        pageCount += 1;
                        if (!await this.loadMoreWorkGraph()) break;
                    }
                    if (this.graphPage.hasMore) {
                        this.graphFocusError = '일부 상하 관계를 아직 불러오지 못했습니다.';
                    }
                    return true;
                } finally {
                    this.graphFocusLoading = false;
                }
            },

            showGraphChildren(node) {
                if (!node) return;
                this.focusGraphNode(node.key, false);
            },

            focusGraphInspector() {
                const focus = () => {
                    if (typeof document === 'undefined') return;
                    const inspector = document.querySelector('[data-graph-inspector]');
                    if (!inspector) return;
                    const closeButton = inspector.querySelector('[data-graph-inspector-close]');
                    const target = closeButton && typeof closeButton.focus === 'function' ? closeButton : inspector;
                    if (typeof target.focus === 'function') target.focus({ preventScroll: true });
                };
                if (typeof this.$nextTick === 'function') this.$nextTick(focus);
                else focus();
            },

            graphInspectorIsOverlay() {
                if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
                    return Boolean(this.graphInspectorOverlay);
                }
                return window.matchMedia('(max-width: 1120px)').matches;
            },

            refreshGraphInspectorMode() {
                this.graphInspectorOverlay = this.graphInspectorIsOverlay();
                return this.graphInspectorOverlay;
            },

            trapGraphInspectorFocus(event) {
                if (!event || event.key !== 'Tab' || !this.graphInspectorIsOverlay()) return false;
                const inspector = event.currentTarget && event.currentTarget.matches && event.currentTarget.matches('[data-graph-inspector]')
                    ? event.currentTarget
                    : (typeof document !== 'undefined' ? document.querySelector('[data-graph-inspector]') : null);
                if (!inspector || typeof inspector.querySelectorAll !== 'function') return false;
                const focusable = Array.from(inspector.querySelectorAll(
                    'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), details > summary, [tabindex]:not([tabindex="-1"])'
                )).filter(element => {
                    if (!element || element.hidden || element.getAttribute('aria-hidden') === 'true') return false;
                    if (typeof window === 'undefined' || typeof window.getComputedStyle !== 'function') return true;
                    const style = window.getComputedStyle(element);
                    return style.display !== 'none' && style.visibility !== 'hidden';
                });
                if (!focusable.length) return false;
                const active = typeof document !== 'undefined' ? document.activeElement : null;
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                let target = null;
                if (event.shiftKey && (active === first || !inspector.contains(active))) target = last;
                if (!event.shiftKey && (active === last || !inspector.contains(active))) target = first;
                if (!target) return false;
                if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                target.focus({ preventScroll: true });
                return true;
            },

            handleGraphEscape(event) {
                let handled = false;
                if (this.graphDragState) {
                    this.cancelGraphNodeDrag(event);
                    handled = true;
                } else if (this.graphViewportPanState && typeof this.cancelGraphViewportPan === 'function') {
                    this.cancelGraphViewportPan(event);
                    handled = true;
                } else if (this.graphFilterOpen) {
                    this.closeGraphFilterMenu(true);
                    handled = true;
                } else if (this.graphFocusedKey) {
                    this.clearGraphFocus(true);
                    handled = true;
                }
                if (handled && event) {
                    if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                    if (typeof event.stopPropagation === 'function') event.stopPropagation();
                }
                return handled;
            },

            clearGraphFocus(restoreFocus = false) {
                if (!this.graphFocusedKey) return;
                const previousKey = String(this.graphFocusedKey);
                this.graphFocusedKey = '';
                this.graphFocusError = '';
                if (typeof this.clearWorkMonitor === 'function') this.clearWorkMonitor();
                this.syncRoute();
                this.refreshWorkGraphLayout();
                if (restoreFocus) {
                    let target = graphFocusReturn;
                    graphFocusReturn = null;
                    if (!target || typeof target.focus !== 'function' || target.isConnected === false) {
                        target = null;
                        if (typeof document !== 'undefined' && typeof document.querySelectorAll === 'function') {
                            const nodes = Array.from(document.querySelectorAll('[data-graph-node-open]'));
                            target = nodes.find(element => (
                                element && element.dataset && String(element.dataset.graphNodeOpen) === previousKey
                            )) || null;
                        }
                    }
                    if (!target || typeof target.focus !== 'function') return;
                    const focus = () => {
                        if (target.isConnected === false) return;
                        target.focus({ preventScroll: true });
                    };
                    if (typeof this.$nextTick === 'function') this.$nextTick(focus);
                    else focus();
                }
            },

            scrollGraphNodeIntoView(key) {
                this.$nextTick(() => {
                    if (typeof this.centerGraphNodeByID === 'function' && this.$refs && this.$refs.graphViewport) {
                        this.centerGraphNodeByID(key, this.$refs.graphViewport);
                        return;
                    }
                    const nodes = Array.from(document.querySelectorAll('[data-graph-node-key]'));
                    const element = nodes.find(candidate => candidate.dataset.graphNodeKey === String(key));
                    const container = element && element.closest('.stash-graph-scroll');
                    if (!element || !container) return;
                    container.scrollTo({
                        left: Math.max(0, element.offsetLeft - (container.clientWidth - element.offsetWidth) / 2),
                        top: Math.max(0, element.offsetTop - (container.clientHeight - element.offsetHeight) / 2),
                        behavior: 'smooth'
                    });
                });
            },

            async changeWorkGraphNamespace() {
                this.graphFocusedKey = '';
                if (typeof this.clearWorkMonitor === 'function') this.clearWorkMonitor();
                if (typeof this.syncGraphProjectFromNamespace === 'function') this.syncGraphProjectFromNamespace();
                await this.loadWorkGraph(false);
            },

            graphNode(id) {
                return this.graph.nodes.find(item => Number(item.id) === Number(id));
            },

            graphScopeLabel() {
                const scope = this.mapNamespaceSlug || this.graphProjectSlug;
                if (!scope) return '전체 네임스페이스의 작업 순서와 포함 관계입니다.';
                const namespace = this.mapNamespaces.find(item => item.slug === scope);
                return (namespace ? namespace.label : scope) + ' 범위의 작업 순서와 포함 관계입니다.';
            },

            graphAriaLabel() {
                const visible = this.workGraphLayout.visibleNodeCount;
                const source = Math.max(this.workGraphLayout.sourceNodeCount, Number(this.graphPage.totalNodes) || 0);
                const disconnected = this.workGraphLayout.disconnected.length;
                const cycles = this.workGraphLayout.cycles.length;
                const partial = this.graphPage.hasMore ? ' 일부 작업과 연결을 아직 불러오지 않았습니다.' : '';
                return `${this.graphScopeLabel()} 작업 ${visible}/${source}개, 연결 ${this.workGraphLayout.edges.length}/${Math.max(this.workGraphLayout.edges.length, Number(this.graphPage.totalEdges) || 0)}개, 연결 없는 작업 ${disconnected}개, 순환 묶음 ${cycles}개.${partial}`;
            },

            graphCanvasViewBox() {
                return `0 0 ${this.workGraphLayout.width} ${this.workGraphLayout.height}`;
            },

            edgeMarkerId(edge) {
                const tone = String(edge && (edge.tone || edge.type) || 'relation')
                    .replace(/[^a-zA-Z0-9_-]/g, '-');
                return 'stash-work-graph-arrow-' + tone;
            },

            edgeMarkerUrl(edge) {
                return `url(#${this.edgeMarkerId(edge)})`;
            },

            graphEdgesMarkup() {
                return this.workGraphLayout.edges.map(edge => {
                    const classes = Object.entries(this.graphEdgeClasses(edge))
                        .filter(([, enabled]) => enabled)
                        .map(([name]) => name)
                        .join(' ');
                    const dash = edge.dashArray
                        ? ` stroke-dasharray="${escapeSVGAttribute(edge.dashArray)}"`
                        : '';
                    const marker = edge.marker
                        ? ` marker-end="${escapeSVGAttribute(this.edgeMarkerUrl(edge))}"`
                        : '';
                    return `<path class="stash-graph-edge ${escapeSVGAttribute(classes)}" d="${escapeSVGAttribute(edge.path)}" stroke="${escapeSVGAttribute(edge.stroke)}" stroke-width="1.7"${dash}${marker}></path>`;
                }).join('');
            },

            graphEdgeClasses(edge) {
                const focused = String(this.graphFocusedKey || '');
                const hierarchyPath = new Set(this.graphFocusedPath().map(item => String(item.id)));
                const dependencyPath = this.graphFocusedDependencyPath();
                const upstream = Boolean(edge && dependencyPath.upstreamEdges.has(String(edge.key)));
                const downstream = Boolean(edge && dependencyPath.downstreamEdges.has(String(edge.key)));
                return {
                    ['is-' + String(edge && (edge.tone || edge.type) || 'relation').replace(/[^a-z0-9_-]/gi, '')]: true,
                    'is-active': Boolean(focused && edge && (
                        upstream || downstream ||
                        (edge.type === 'part_of' && hierarchyPath.has(String(edge.fromKey)) && hierarchyPath.has(String(edge.toKey)))
                    )),
                    'is-upstream': upstream,
                    'is-downstream': downstream,
                    'is-cycle': Boolean(edge && edge.cycle)
                };
            },

            graphNodeClasses(node) {
                const dependencyPath = this.graphFocusedDependencyPath();
                const hierarchyPath = this.graphFocusedPath().some(item => String(item.id) === String(node && node.key));
                return {
                    ['is-' + String(node && node.item && node.item.status || 'unknown').replace(/[^a-z0-9_-]/gi, '')]: true,
                    'is-cycle': Boolean(node && node.cycle),
                    'is-moved': Boolean(node && node.offset && (node.offset.x || node.offset.y)),
                    'is-context': Boolean(node && node.context),
                    'is-path': Boolean(node && (dependencyPath.nodes.has(String(node.key)) || hierarchyPath)),
                    'is-upstream': Boolean(node && dependencyPath.upstreamNodes.has(String(node.key))),
                    'is-downstream': Boolean(node && dependencyPath.downstreamNodes.has(String(node.key))),
                    'is-focused': Boolean(node && String(node.key) === String(this.graphFocusedKey))
                };
            },

            graphNodeMeta(node) {
                if (!node || !node.item) return '';
                const parts = [];
                const parent = this.graphParentFor(node.item);
                const children = this.graphChildrenFor(node.item);
                if (parent) parts.push('상위 ' + (parent.issue_key || '#' + parent.id));
                if (children.length) parts.push(`하위 ${children.length}개`);
                const owner = String(node.item.owner || '').trim();
                if (owner) parts.push(owner);
                if (node.context) parts.push('연결 맥락');
                return parts.join(' · ') || '독립 작업';
            },

            graphNodeAriaLabel(node) {
                const item = node.item;
                const key = item.issue_key || '#' + item.id;
                const parts = [`${key}: ${item.title}`, `상태 ${this.statusLabel(item.status)}`];
                const parent = this.graphParentFor(item);
                const children = this.graphChildrenFor(item);
                if (parent) parts.push(`상위 작업 ${parent.issue_key || '#' + parent.id}`);
                if (children.length) parts.push(`하위 작업 ${children.length}개`);
                if (node.cycle) parts.push('서로 막는 연결에 포함됨');
                if (node.orphanParentId !== undefined) parts.push(`상위 작업 ${node.orphanParentId}을 찾을 수 없음`);
                if (node.hierarchyCycle) parts.push('상위 작업 연결이 순환함');
                if (node.context) parts.push('검색 결과의 연결 맥락');
                return parts.join('. ') + '.';
            },

            async loadWorkGraph(refreshNamespaces = true) {
                this.activeNav = 'graph';
                this.resultTitle = '작업 흐름';
                this.resultDescription = '작업의 선후 관계와 상하 구조를 봅니다.';
                this.view = 'graph';
                this.graphError = '';
                this.loading = true;
                await this.loadMapNamespaces(refreshNamespaces);
                if (typeof this.syncGraphProjectFromNamespace === 'function') this.syncGraphProjectFromNamespace();
                await this.loadWorkView('graph');
                this.$nextTick(() => {
                    if (typeof this.scheduleGraphViewportFit !== 'function' || !this.$refs || !this.$refs.graphViewport) return;
                    this.scheduleGraphViewportFit(this.$refs.graphViewport, this.workGraphLayout);
                });
            }
        };
    }


    return { createWorkGraphViewModel };
}));
