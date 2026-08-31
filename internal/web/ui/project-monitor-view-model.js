(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./search-utils.js'))
        : factory(root.StashSearch);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashProjectMonitorViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (searchUtils) {
    'use strict';

    if (!searchUtils) throw new Error('검색 모듈을 불러오지 못했습니다.');

    const text = value => String(value || '').trim();
    const emptyMap = () => ({
        goal_tree: { root_goal_id: null, goals: [] },
        work_items: [], unassigned_work: [], edges: []
    });

    function createProjectMonitorViewModel() {
        let projectMonitorRequestSequence = 0;
        return {
            projectMonitorProjectSlug: '',
            projectMonitorMap: emptyMap(),
            projectMonitorItemList: [],
            projectMonitorBlockersByWork: {},
            projectMonitorFilter: { query: '', status: '', agent: '' },
            projectMonitorSelectedID: 0,
            projectMonitorLoading: false,
            projectMonitorError: '',

            projectMonitorProjects() {
                return (this.mapNamespaces || []).filter(item => /^\/projects\/[^/]+$/.test(text(item && item.slug)));
            },

            setProjectMonitorMap(value) {
                const source = value && typeof value === 'object' ? value : {};
                const tree = source.goal_tree && typeof source.goal_tree === 'object' ? source.goal_tree : {};
                const normalized = {
                    goal_tree: {
                        root_goal_id: tree.root_goal_id === undefined ? null : tree.root_goal_id,
                        goals: Array.isArray(tree.goals) ? tree.goals : []
                    },
                    work_items: Array.isArray(source.work_items) ? source.work_items : [],
                    unassigned_work: Array.isArray(source.unassigned_work) ? source.unassigned_work : [],
                    edges: Array.isArray(source.edges) ? source.edges : []
                };
                const items = new Map();
                for (const item of [...normalized.work_items, ...normalized.unassigned_work]) {
                    const id = Number(item && item.id) || 0;
                    if (id) items.set(id, item);
                }
                const itemsByKey = new Map(Array.from(items.values()).map(item => [`work:${Number(item.id)}`, item]));
                const blockers = {};
                for (const edge of normalized.edges) {
                    if (!edge || edge.relation !== 'blocks') continue;
                    const blocker = itemsByKey.get(edge.from);
                    const target = itemsByKey.get(edge.to);
                    if (!blocker || !target || blocker.status === 'done' || blocker.status === 'canceled') continue;
                    const id = String(target.id);
                    blockers[id] = [...(blockers[id] || []), blocker];
                }
                this.projectMonitorMap = normalized;
                this.projectMonitorItemList = Array.from(items.values());
                this.projectMonitorBlockersByWork = blockers;
            },

            clearProjectMonitorMap() {
                this.setProjectMonitorMap(emptyMap());
            },

            projectMonitorItems() {
                return this.projectMonitorItemList;
            },

            projectMonitorSearchValues(item) {
                const goalID = Number(item && item.goal_id) || 0;
                const goal = this.projectMonitorMap.goal_tree.goals.find(candidate => Number(candidate.id) === goalID);
                return [
                    item && item.issue_key, item && item.title, item && item.description,
                    item && item.status, item && item.owner, item && item.agent_id,
                    item && item.reporter, item && item.issue_type, item && item.due_at,
                    item && item.latest_result, item && item.next_action,
                    item && item.labels, item && item.required_capabilities,
                    goal && goal.content
                ];
            },

            projectMonitorRows() {
                const query = text(this.projectMonitorFilter.query);
                const status = text(this.projectMonitorFilter.status);
                const agent = text(this.projectMonitorFilter.agent);
                const rank = value => ({ expired: 0, blocked: 1, doing: 2, review: 3, ready: 4, backlog: 5, done: 6, canceled: 7 })[value] ?? 8;
                return this.projectMonitorItems()
                    .filter(item => !query || searchUtils.matchesSearch(this.projectMonitorSearchValues(item), query))
                    .filter(item => !status || this.projectMonitorDisplayStatus(item) === status)
                    .filter(item => !agent || text(item.agent_id || item.owner) === agent)
                    .sort((left, right) => (
                        rank(this.projectMonitorDisplayStatus(left)) - rank(this.projectMonitorDisplayStatus(right)) ||
                        Number(right.priority || 0) - Number(left.priority || 0) ||
                        Number(left.id) - Number(right.id)
                    ));
            },

            projectMonitorAgents() {
                const agents = new Set(this.projectMonitorItems().map(item => text(item.agent_id || item.owner)).filter(Boolean));
                return Array.from(agents).sort((left, right) => left.localeCompare(right, 'ko'));
            },

            projectMonitorCount(status) {
                const items = this.projectMonitorItems();
                if (!status) return items.length;
                return items.filter(item => this.projectMonitorDisplayStatus(item) === status).length;
            },

            projectMonitorLeaseExpired(item) {
                const expiresAt = new Date(item && item.lease_expires_at || '');
                return text(item && item.attempt_status) === 'active' &&
                    !Number.isNaN(expiresAt.getTime()) && expiresAt.getTime() <= Date.now();
            },

            projectMonitorDisplayStatus(item) {
                return this.projectMonitorLeaseExpired(item) ? 'expired' : text(item && item.status);
            },

            projectMonitorIsBlocked(item) {
                return text(item && item.status) === 'blocked';
            },

            projectMonitorBlockerLabel(item) {
                return text(item && (item.issue_key || item.title)) || (item && item.id ? '#' + item.id : '알 수 없는 작업');
            },

            projectMonitorRootGoal() {
                const rootID = Number(this.projectMonitorMap.goal_tree.root_goal_id) || 0;
                return this.projectMonitorMap.goal_tree.goals.find(goal => Number(goal.id) === rootID) || null;
            },

            projectMonitorProgress(value) {
                return Math.round(Math.max(0, Math.min(1, Number(value) || 0)) * 100) + '%';
            },

            projectMonitorAgent(item) {
                return text(item && (item.agent_id || item.owner)) || '지정 안 됨';
            },

            projectMonitorAttemptLabel(item) {
                const status = text(item && item.attempt_status);
                if (this.projectMonitorLeaseExpired(item)) return '연결 만료';
                return ({
                    active: '작업 중', handed_off: '이어받기 대기', completed: '작업 종료',
                    abandoned: '중단', canceled: '취소', expired: '연결 만료'
                })[status] || (item && item.status === 'doing' ? '작업 중' : '대기');
            },

            projectMonitorBlockers(item) {
                return this.projectMonitorBlockersByWork[String(Number(item && item.id) || 0)] || [];
            },

            projectMonitorSelectedItem() {
                const id = Number(this.projectMonitorSelectedID) || 0;
                return this.projectMonitorItems().find(item => Number(item.id) === id) || null;
            },

            applyProjectMonitorBrief(workItemID) {
                const brief = this.workMonitorBrief && typeof this.workMonitorBrief === 'object' ? this.workMonitorBrief : {};
                const workItem = brief.work_item && typeof brief.work_item === 'object' ? brief.work_item : {};
                const id = Number(workItemID) || 0;
                if (!id || Number(workItem.id) !== id) return;
                const attempt = brief.latest_attempt && typeof brief.latest_attempt === 'object' ? brief.latest_attempt : {};
                const checkpoint = brief.latest_checkpoint && typeof brief.latest_checkpoint === 'object' ? brief.latest_checkpoint : {};
                const replace = item => Number(item && item.id) === id ? {
                    ...item,
                    status: text(workItem.status) || item.status,
                    owner: text(workItem.owner) || item.owner,
                    agent_id: text(attempt.agent_id) || item.agent_id,
                    attempt_status: text(attempt.status) || item.attempt_status,
                    lease_expires_at: attempt.lease_expires_at || item.lease_expires_at,
                    latest_result: text(checkpoint.result) || item.latest_result,
                    next_action: text(brief.next_action || checkpoint.next_action) || item.next_action
                } : item;
                this.setProjectMonitorMap({
                    ...this.projectMonitorMap,
                    work_items: this.projectMonitorMap.work_items.map(replace),
                    unassigned_work: this.projectMonitorMap.unassigned_work.map(replace)
                });
            },

            projectMonitorHasFilters() {
                return Boolean(text(this.projectMonitorFilter.query) || text(this.projectMonitorFilter.status) || text(this.projectMonitorFilter.agent));
            },

            resetProjectMonitorFilters() {
                this.projectMonitorFilter = { query: '', status: '', agent: '' };
                this.syncRoute();
            },

            projectMonitorFilterChanged() {
                this.syncRoute();
            },

            async selectProjectMonitorWork(item) {
                const id = Number(item && item.id) || 0;
                if (!id) return;
                this.projectMonitorSelectedID = id;
                this.syncRoute();
                await this.loadWorkMonitor(id);
                this.applyProjectMonitorBrief(id);
            },

            clearProjectMonitorSelection() {
                this.projectMonitorSelectedID = 0;
                this.clearWorkMonitor();
                this.syncRoute();
            },

            async changeProjectMonitorProject() {
                this.projectMonitorSelectedID = 0;
                this.clearWorkMonitor();
                await this.loadProjectMonitor(false);
            },

            async showProjectMonitorWorkFlow(item) {
                const id = Number(item && item.id) || 0;
                if (!id || !this.projectMonitorProjectSlug) return;
                this.graphProjectSlug = this.projectMonitorProjectSlug;
                this.mapNamespaceSlug = '';
                this.graphFocusedKey = String(id);
                await this.loadWorkGraph(false);
                await this.focusGraphNodeByID(id);
            },

            async loadProjectMonitor(refreshNamespaces = true) {
                const requestSequence = ++projectMonitorRequestSequence;
                this.activeNav = 'monitor';
                this.resultTitle = '작업 관제';
                this.resultDescription = '에이전트별 진행 상황과 다음 할 일을 봅니다.';
                this.view = 'monitor';
                this.projectMonitorError = '';
                this.projectMonitorLoading = true;
                this.loading = true;
                try {
                    await this.loadMapNamespaces(refreshNamespaces);
                    if (requestSequence !== projectMonitorRequestSequence) return;
                    if (this.mapNamespaceError) throw new Error(this.mapNamespaceError);
                    const projects = this.projectMonitorProjects();
                    if (!projects.some(item => item.slug === this.projectMonitorProjectSlug)) {
                        const scoped = projects.find(item => this.mapNamespaceSlug === item.slug || this.mapNamespaceSlug.startsWith(item.slug + '/'));
                        this.projectMonitorProjectSlug = scoped ? scoped.slug : (projects.length === 1 ? projects[0].slug : '');
                    }
                    if (!this.projectMonitorProjectSlug) {
                        this.clearProjectMonitorMap();
                        this.projectMonitorSelectedID = 0;
                        this.clearWorkMonitor();
                        return;
                    }
                    const data = await this.invokeTool('get_goal_map', {
                        namespace: this.projectMonitorProjectSlug, include_done: true
                    });
                    if (requestSequence !== projectMonitorRequestSequence) return;
                    const value = this.toolValue(data) || {};
                    this.setProjectMonitorMap(value);
                    if (this.projectMonitorFilter.agent && !this.projectMonitorAgents().includes(this.projectMonitorFilter.agent)) {
                    this.projectMonitorFilter.agent = '';
                    }
                    const selected = this.projectMonitorSelectedItem();
                    if (selected) {
                        await this.loadWorkMonitor(selected.id);
                        if (requestSequence !== projectMonitorRequestSequence) return;
                        this.applyProjectMonitorBrief(selected.id);
                    }
                    else {
                        this.projectMonitorSelectedID = 0;
                        this.clearWorkMonitor();
                    }
                    this.resultValue = this.projectMonitorMap;
                    this.resultKind = 'get_goal_map';
                    this.result = JSON.stringify(this.projectMonitorMap, null, 2);
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (error) {
                    if (requestSequence !== projectMonitorRequestSequence) return;
                    this.clearProjectMonitorMap();
                    this.projectMonitorError = error.message || '작업 현황을 불러오지 못했습니다.';
                    this.result = '오류: ' + this.projectMonitorError;
                } finally {
                    if (requestSequence === projectMonitorRequestSequence) {
                        this.projectMonitorLoading = false;
                        this.loading = false;
                        this.syncRoute();
                    }
                }
            }
        };
    }

    return { createProjectMonitorViewModel };
}));
