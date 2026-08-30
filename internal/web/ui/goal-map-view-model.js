(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./goal-map-layout.js'))
        : factory(root.StashGoalMap);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashGoalMapViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (goalMapLayout) {
    'use strict';

    function createGoalMapViewModel() {
        const layout = goalMapLayout || {
            emptyLayout: () => ({ width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], rings: [], focusKey: '', counts: { resource: 0, memory: 0, work: 0, goal: 0 } }),
            filterGoalMap: value => value,
            buildGoalMapLayout: () => ({ width: 0, height: 0, canvasStyle: '', nodes: [], edges: [], rings: [], focusKey: '', counts: { resource: 0, memory: 0, work: 0, goal: 0 } })
        };
        const emptyMap = () => ({
            goal_tree: { root_goal_id: null, goals: [] },
            root_candidates: [], root_candidates_truncated: false,
            work_items: [], resources: [], resource_total: 0, resources_truncated: false,
            memories: [], edges: [], unassigned_work: []
        });
        return {
            goalMap: emptyMap(),
            goalMapVisible: emptyMap(),
            goalMapLayout: layout.emptyLayout(),
            goalMapFilters: {
                query: '', status: '', agent: '', memoryType: '',
                kinds: { goal: true, work: true, memory: true, resource: true }
            },
            goalMapFilterOpen: false,
            goalMapError: '',
            goalMapRootCandidates: [],
            goalMapRootGoalId: '',
            goalMapRootSaving: false,

            setGoalMap(value) {
                const source = value && typeof value === 'object' ? value : {};
                const tree = source.goal_tree && typeof source.goal_tree === 'object' ? source.goal_tree : {};
                this.goalMap = {
                    goal_tree: {
                        root_goal_id: tree.root_goal_id === undefined ? null : tree.root_goal_id,
                        goals: Array.isArray(tree.goals) ? tree.goals : []
                    },
                    root_candidates: Array.isArray(source.root_candidates) ? source.root_candidates : [],
                    root_candidates_truncated: Boolean(source.root_candidates_truncated),
                    work_items: Array.isArray(source.work_items) ? source.work_items : [],
                    resources: Array.isArray(source.resources) ? source.resources : [],
                    resource_total: Number(source.resource_total) || 0,
                    resources_truncated: Boolean(source.resources_truncated),
                    memories: Array.isArray(source.memories) ? source.memories : [],
                    edges: Array.isArray(source.edges) ? source.edges : [],
                    unassigned_work: Array.isArray(source.unassigned_work) ? source.unassigned_work : []
                };
                this.goalMapRootCandidates = this.goalMap.root_candidates;
                if (!this.goalMapRootCandidates.some(goal => String(goal.id) === String(this.goalMapRootGoalId))) {
                    this.goalMapRootGoalId = '';
                }
                this.refreshGoalMapLayout();
            },

            clearGoalMap() {
                this.setGoalMap(emptyMap());
            },

            goalMapHasRoot() {
                return this.goalMap.goal_tree.root_goal_id !== null && this.goalMap.goal_tree.goals.length > 0;
            },

            refreshGoalMapLayout() {
                this.goalMapVisible = layout.filterGoalMap(this.goalMap, this.goalMapFilters);
                this.goalMapLayout = layout.buildGoalMapLayout(this.goalMapVisible);
                if (!this.loading && this.view === 'goal-map') this.syncRoute(true);
            },

            goalMapAgents() {
                const values = this.goalMap.work_items.map(item => String(item.agent_id || item.owner || '').trim()).filter(Boolean);
                return Array.from(new Set(values)).sort((left, right) => left.localeCompare(right, 'ko'));
            },

            toggleGoalMapKind(kind) {
                if (!Object.prototype.hasOwnProperty.call(this.goalMapFilters.kinds, kind)) return;
                this.goalMapFilters = {
                    ...this.goalMapFilters,
                    kinds: { ...this.goalMapFilters.kinds, [kind]: !this.goalMapFilters.kinds[kind] }
                };
                this.refreshGoalMapLayout();
            },

            goalMapHasFilters() {
                const filters = this.goalMapFilters;
                return Boolean(
                    String(filters.query || '').trim() || String(filters.status || '').trim() ||
                    String(filters.agent || '').trim() || String(filters.memoryType || '').trim() ||
                    Object.values(filters.kinds).some(value => !value)
                );
            },

            resetGoalMapFilters() {
                if (!this.goalMapHasFilters()) return;
                this.goalMapFilters = {
                    query: '', status: '', agent: '', memoryType: '',
                    kinds: { goal: true, work: true, memory: true, resource: true }
                };
                this.refreshGoalMapLayout();
            },

            goalMapFilterCount() {
                const filters = this.goalMapFilters;
                return Number(Boolean(String(filters.query || '').trim())) +
                    Number(Boolean(String(filters.status || '').trim())) +
                    Number(Boolean(String(filters.agent || '').trim())) +
                    Number(Boolean(String(filters.memoryType || '').trim())) +
                    Object.values(filters.kinds).filter(value => value === false).length;
            },

            goalMapFilterChips() {
                const filters = this.goalMapFilters;
                const chips = [];
                const query = String(filters.query || '').trim();
                if (query) chips.push({ key: 'query', label: `검색: ${query}` });
                if (filters.status) chips.push({ key: 'status', label: `상태: ${this.statusLabel(filters.status)}` });
                if (filters.agent) chips.push({ key: 'agent', label: `담당: ${filters.agent}` });
                if (filters.memoryType) chips.push({ key: 'memoryType', label: `기억: ${this.goalMapMemoryTypeLabel(filters.memoryType)}` });
                const labels = { goal: '목표', work: '작업', memory: '사실·기억', resource: '자료' };
                Object.keys(labels).filter(kind => filters.kinds[kind] === false).forEach(kind => {
                    chips.push({ key: `kind:${kind}`, label: `${labels[kind]} 숨김` });
                });
                return chips;
            },

            clearGoalMapFilter(key) {
                if (['query', 'status', 'agent', 'memoryType'].includes(key)) this.goalMapFilters[key] = '';
                else if (String(key || '').startsWith('kind:')) {
                    const kind = String(key).slice('kind:'.length);
                    if (Object.prototype.hasOwnProperty.call(this.goalMapFilters.kinds, kind)) {
                        this.goalMapFilters = {
                            ...this.goalMapFilters,
                            kinds: { ...this.goalMapFilters.kinds, [kind]: true }
                        };
                    }
                }
                this.refreshGoalMapLayout();
            },

            goalMapScopeLabel() {
                if (!this.mapNamespaceSlug) return '기본 공간(/)의 목표와 연결 근거입니다.';
                const namespace = this.mapNamespaces.find(item => item.slug === this.mapNamespaceSlug);
                return (namespace ? namespace.label : this.mapNamespaceSlug) + ' 범위의 목표와 연결 근거입니다.';
            },

            goalMapStatusCount(status) {
                if (status === 'doing') return this.goalMapVisible.work_items.filter(item => item.status === 'doing' || item.status === 'review').length;
                return this.goalMapVisible.work_items.filter(item => item.status === status).length;
            },

            goalMapAttentionItems() {
                return this.goalMapVisible.work_items
                    .filter(item => item.status === 'doing' || item.status === 'review' || item.status === 'blocked')
                    .sort((left, right) => {
                        const rank = value => value === 'blocked' ? 0 : 1;
                        return rank(left.status) - rank(right.status) || Number(left.id) - Number(right.id);
                    })
                    .slice(0, 8);
            },

            goalMapGoalKind(goal) {
                if (Number(goal && goal.id) === Number(this.goalMap.goal_tree.root_goal_id)) return '공통 목표';
                return Math.max(1, Number(goal && goal.depth) || 1) + '단계 목표';
            },

            goalMapProgressPercent(value) {
                const progress = Math.max(0, Math.min(1, Number(value) || 0));
                return Math.round(progress * 100) + '%';
            },

            goalMapGoalProgressText(goal) {
                if ((Number(goal && goal.subtree_work_total) || 0) > 0) return `작업 ${Number(goal.subtree_work_done) || 0}/${Number(goal.subtree_work_total) || 0}`;
                if ((Number(goal && goal.child_goal_total) || 0) > 0) return `하위 목표 ${Number(goal.child_goal_completed) || 0}/${Number(goal.child_goal_total) || 0}`;
                return '연결된 작업 없음';
            },

            goalMapMemoryTypeLabel(value) {
                return ({ episode: '경험', fact: '사실', hypothesis: '가설', failure: '실패 기록' })[value] || '기억';
            },

            goalMapMemoryStatusLabel(value) {
                return ({ active: '유효', recorded: '기록됨', proposed: '검토 전', confirmed: '확인됨', rejected: '기각' })[value] || value || '기록됨';
            },

            goalMapResourceSourceLabel(item) {
                const source = String(item && item.source || '').trim();
                return ({ jira: 'Jira', confluence: 'Confluence', git: 'Git', web: '웹', stash: 'Stash' })[source] || source || '연결 자료';
            },

            goalMapResourceAuthorityLabel(item) {
                return item && item.authority === 'external' ? '외부 기준' : 'Stash 기준';
            },

            goalMapWorkMonitorText(item) {
                const agent = String(item && (item.agent_id || item.owner) || '').trim();
                const status = this.statusLabel(item && item.status);
                return agent ? `${status} · ${agent}` : `${status} · 담당 없음`;
            },

            goalMapAttentionText(item) {
                const monitor = this.goalMapWorkMonitorText(item);
                const next = String(item && item.next_action || '').trim();
                return next ? `${monitor} · ${next}` : monitor;
            },

            goalMapWorkDetail(item) {
                const next = String(item && item.next_action || '').trim();
                const result = String(item && item.latest_result || '').trim();
                const details = [];
                if (next) details.push('다음 행동: ' + next);
                if (result) details.push('최근 결과: ' + result);
                const capabilities = Array.isArray(item && item.required_capabilities) ? item.required_capabilities : [];
                if (capabilities.length) details.push('필요 능력: ' + capabilities.join(', '));
                return details.join(' · ');
            },

            async setProjectGoal() {
                const goalId = Number(this.goalMapRootGoalId);
                if (!Number.isInteger(goalId) || goalId <= 0) return;
                this.goalMapRootSaving = true;
                try {
                    await this.invokeTool('set_project_goal', {
                        namespace: this.mapNamespaceSlug || '/', goal_id: goalId
                    });
                    await this.loadGoalMap(false);
                    this.setNotice('공통 목표를 정했습니다.', 'success');
                } catch (error) {
                    this.setNotice(error.message || '공통 목표를 저장하지 못했습니다.', 'error');
                } finally {
                    this.goalMapRootSaving = false;
                }
            },

            goalMapNodeClasses(node) {
                const item = node && node.item || {};
                return {
                    ['is-' + String(node && node.kind || 'unknown').replace(/[^a-z0-9_-]/gi, '')]: true,
                    'is-root': node && node.kind === 'goal' && Number(item.id) === Number(this.goalMap.goal_tree.root_goal_id),
                    'is-complete': item.status === 'completed' || item.status === 'done',
                    'is-ready': Boolean(item.ready_to_complete),
                    'is-mismatch': Boolean(item.completion_mismatch),
                    'is-context': Boolean(item.__filter_context)
                };
            },

            goalMapNodeAriaLabel(node) {
                const item = node && node.item || {};
                if (node.kind === 'goal') return `${this.goalMapGoalKind(item)}: ${item.content}. 진행 ${this.goalMapProgressPercent(item.progress)}. 상태 ${this.statusLabel(item.status)}.`;
                if (node.kind === 'work') {
                    const detail = this.goalMapWorkDetail(item);
                    return `${item.issue_key || '#' + item.id}: ${item.title}. ${this.goalMapWorkMonitorText(item)}.${detail ? ' ' + detail + '.' : ''}`;
                }
                if (node.kind === 'resource') return `${this.goalMapResourceSourceLabel(item)} 자료: ${item.title}. ${this.goalMapResourceAuthorityLabel(item)}.`;
                return `${this.goalMapMemoryTypeLabel(item.memory_type)}: ${item.content}.`;
            },

            goalMapCanvasViewBox() {
                return `0 0 ${this.goalMapLayout.width} ${this.goalMapLayout.height}`;
            },

            goalMapMarkerId(edge) {
                return 'stash-goal-map-arrow-' + String(edge && edge.key || '').replace(/[^a-zA-Z0-9_-]/g, '-');
            },

            goalMapMarkerUrl(edge) {
                return `url(#${this.goalMapMarkerId(edge)})`;
            },

            goalMapAriaLabel() {
                const counts = this.goalMapLayout.counts;
                return `${this.goalMapScopeLabel()} 목표 ${counts.goal}개, 작업 ${counts.work}개, 연결 자료 ${counts.resource}개, 기억 ${counts.memory}개, 막힌 작업 ${this.goalMapStatusCount('blocked')}개.`;
            },

            async loadGoalMap(refreshNamespaces = true) {
                this.activeNav = 'goal-map';
                this.resultTitle = '목표·지식 지도';
                this.resultDescription = '공통 목표를 중심으로 세부 목표, 작업, 사실과 자료를 봅니다.';
                this.view = 'goal-map';
                this.goalMapError = '';
                this.loading = true;
                try {
                    await this.loadMapNamespaces(refreshNamespaces);
                    const data = await this.invokeTool('get_goal_map', {
                        namespace: this.mapNamespaceSlug || '/', include_done: true
                    });
                    const value = this.toolValue(data) || {};
                    this.setGoalMap(value);
                    this.resultValue = this.goalMap;
                    this.resultKind = 'get_goal_map';
                    this.result = JSON.stringify(this.goalMap, null, 2);
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (error) {
                    this.clearGoalMap();
                    this.goalMapError = error.message || '목표·지식 지도를 불러오지 못했습니다.';
                    this.result = '오류: ' + this.goalMapError;
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            }
        };
    }


    return { createGoalMapViewModel };
}));
