(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./work-graph-layout.js'))
        : factory(root.StashWorkGraph);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashWorkGraphViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (workGraphLayout) {
    'use strict';

    if (!workGraphLayout) throw new Error('작업 흐름 배치 모듈을 불러오지 못했습니다.');

    function createWorkGraphViewModel() {
        const layout = workGraphLayout;
        const defaultRelations = () => ({ part_of: true, blocks: true, relates_to: true });
        const relationGroup = type => type === 'part_of' || type === 'blocks' ? type : 'relates_to';
        let activeDragHandle = null;
        return {
            graph: { nodes: [], edges: [], worktrees: [] },
            workGraphLayout: layout.emptyLayout(),
            graphFilter: { query: '', status: '', relations: defaultRelations() },
            graphFilterOpen: false,
            graphFocusedKey: '',
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
                const validIDs = new Set(this.graph.nodes.map(item => String(item.id)));
                const validOffsetKeys = new Set(Array.from(validIDs).map(id => `node:${id}`));
                this.graphNodeOffsets = Object.fromEntries(
                    Object.entries(this.graphNodeOffsets).filter(([key, offset]) => (
                        validOffsetKeys.has(key) && offset && (Number(offset.x) || Number(offset.y))
                    ))
                );
                if (this.graph.nodes.length && this.graphFocusedKey && !validIDs.has(String(this.graphFocusedKey))) {
                    this.graphFocusedKey = '';
                }
                this.refreshWorkGraphLayout();
            },

            clearWorkGraph() {
                this.setWorkGraph({ nodes: [], edges: [], worktrees: [] });
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
                const query = String(this.graphFilter.query || '').trim().toLocaleLowerCase('ko');
                const status = String(this.graphFilter.status || '').trim();
                const hasNodeFilters = Boolean(query || status);
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                const relationIsVisible = edge => relations[relationGroup(String(edge && edge.edge_type || 'relates_to'))] !== false;
                const byID = new Map(this.graph.nodes.map(item => [String(item.id), item]));
                const matchedIDs = new Set(this.graph.nodes.filter(item => {
                    if (status && item.status !== status) return false;
                    if (!query) return true;
                    const values = [
                        item.issue_key, item.title, item.description, item.status, item.owner,
                        ...(Array.isArray(item.labels) ? item.labels : []),
                        ...(Array.isArray(item.required_capabilities) ? item.required_capabilities : [])
                    ];
                    return values.some(value => String(value || '').toLocaleLowerCase('ko').includes(query));
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
                this.writeGraphNodeOffset(
                    state.key,
                    state.originX + event.clientX - state.startX,
                    state.originY + event.clientY - state.startY,
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

            graphHasFilters() {
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                return Boolean(
                    String(this.graphFilter.query || '').trim() || String(this.graphFilter.status || '').trim() ||
                    Object.values(relations).some(value => value === false)
                );
            },

            resetGraphFilters() {
                if (!this.graphHasFilters()) return;
                this.graphFilter = { query: '', status: '', relations: defaultRelations() };
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
                    Object.values(relations).filter(value => value === false).length;
            },

            graphFilterChips() {
                const chips = [];
                const query = String(this.graphFilter.query || '').trim();
                const status = String(this.graphFilter.status || '').trim();
                if (query) chips.push({ key: 'query', label: `검색: ${query}` });
                if (status) chips.push({ key: 'status', label: `상태: ${this.statusLabel(status)}` });
                const labels = { part_of: '상하 관계', blocks: '선후 관계', relates_to: '관련 관계' };
                const relations = { ...defaultRelations(), ...(this.graphFilter.relations || {}) };
                Object.keys(labels).filter(relation => relations[relation] === false).forEach(relation => {
                    chips.push({ key: `relation:${relation}`, label: `${labels[relation]} 숨김` });
                });
                return chips;
            },

            clearGraphFilter(key) {
                if (key === 'query') this.graphFilter.query = '';
                else if (key === 'status') this.graphFilter.status = '';
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

            focusGraphNode(key, scroll = true) {
                const normalized = String(key === undefined || key === null ? '' : key);
                if (!normalized || !this.graph.nodes.some(item => String(item.id) === normalized)) return;
                const changed = this.graphFocusedKey !== normalized;
                this.graphFocusedKey = normalized;
                if (changed) this.syncRoute();
                this.refreshWorkGraphLayout();
                if (scroll) this.scrollGraphNodeIntoView(normalized);
                this.graphMoveAnnouncement = `${this.graphWorkLabel(this.graphFocusedItem())} 선택`;
            },

            focusGraphNodeByID(id) {
                this.focusGraphNode(String(id));
            },

            showGraphChildren(node) {
                if (!node) return;
                this.focusGraphNode(node.key, false);
            },

            clearGraphFocus() {
                if (!this.graphFocusedKey) return;
                this.graphFocusedKey = '';
                this.syncRoute();
                this.refreshWorkGraphLayout();
            },

            scrollGraphNodeIntoView(key) {
                this.$nextTick(() => {
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
                await this.loadWorkGraph(false);
            },

            graphNode(id) {
                return this.graph.nodes.find(item => Number(item.id) === Number(id));
            },

            graphScopeLabel() {
                if (!this.mapNamespaceSlug) return '전체 네임스페이스의 작업 순서와 포함 관계입니다.';
                const namespace = this.mapNamespaces.find(item => item.slug === this.mapNamespaceSlug);
                return (namespace ? namespace.label : this.mapNamespaceSlug) + ' 범위의 작업 순서와 포함 관계입니다.';
            },

            graphAriaLabel() {
                const visible = this.workGraphLayout.visibleNodeCount;
                const source = this.workGraphLayout.sourceNodeCount;
                const disconnected = this.workGraphLayout.disconnected.length;
                const cycles = this.workGraphLayout.cycles.length;
                return `${this.graphScopeLabel()} 작업 ${visible}/${source}개, 연결 ${this.workGraphLayout.edges.length}개, 연결 없는 작업 ${disconnected}개, 순환 묶음 ${cycles}개.`;
            },

            graphCanvasViewBox() {
                return `0 0 ${this.workGraphLayout.width} ${this.workGraphLayout.height}`;
            },

            edgeMarkerId(edge) {
                return 'stash-work-graph-arrow-' + String(edge && edge.key || '').replace(/[^a-zA-Z0-9_-]/g, '-');
            },

            edgeMarkerUrl(edge) {
                return `url(#${this.edgeMarkerId(edge)})`;
            },

            graphNodeClasses(node) {
                return {
                    ['is-' + String(node && node.item && node.item.status || 'unknown').replace(/[^a-z0-9_-]/gi, '')]: true,
                    'is-cycle': Boolean(node && node.cycle),
                    'is-moved': Boolean(node && node.offset && (node.offset.x || node.offset.y)),
                    'is-context': Boolean(node && node.context),
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

            graphNodeRoleLabel(node) {
                if (node && node.isEntry) return '시작점';
                if (node && node.isOutcome) return '최종점';
                return '';
            },

            graphNodeAriaLabel(node) {
                const item = node.item;
                const key = item.issue_key || '#' + item.id;
                const parts = [`${key}: ${item.title}`, `상태 ${this.statusLabel(item.status)}`];
                const parent = this.graphParentFor(item);
                const children = this.graphChildrenFor(item);
                const role = this.graphNodeRoleLabel(node);
                if (role) parts.push(role);
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
                await this.loadWorkView('graph');
            }
        };
    }


    return { createWorkGraphViewModel };
}));
