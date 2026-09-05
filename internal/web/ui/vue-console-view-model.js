(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashVueConsoleViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createViewModel({ api, routeAPI, goalMap, workGraph, search, window, i18n = typeof module === 'object' && module.exports ? require('./console-i18n.js') : window.StashI18n }) {
        const root = window;
        const { document, navigator } = window;
        const text = value => String(value == null ? '' : value).trim();
        const number = value => Number.isFinite(Number(value)) ? Number(value) : 0;
        const isProject = value => /^\/projects\/[^/]+$/.test(text(value));
        const statusNames = {
            backlog: 'status.backlog', ready: 'status.ready', active: 'status.doing', doing: 'status.doing', blocked: 'status.blocked',
            review: 'status.review', done: 'status.done', canceled: 'status.canceled', expired: 'status.expired',
            completed: 'status.done', abandoned: 'status.abandoned', proposed: 'status.proposed', testing: 'status.testing',
            confirmed: 'status.confirmed', rejected: 'status.rejected', recorded: 'status.recorded'
        };
        const kindNames = { goal: 'kind.goal', work: 'kind.work', memory: 'kind.memory', resource: 'kind.resource' };
        const navItems = [
            { route: 'goal-map', label: 'nav.overview', icon: '◎' },
            { route: 'plan', label: 'nav.plan', icon: '◇' },
            { route: 'monitor', label: 'nav.monitor', icon: '◉' },
            { route: 'board', label: 'nav.board', icon: '▦' },
            { route: 'graph', label: 'nav.graph', icon: '⌁' },
            { route: 'worktrees', label: 'nav.git', icon: '⌘' }
        ];
        const dataTools = {
            list_memories: 'memory',
            query_facts: 'memory',
            list_hypotheses: 'memory',
            list_goals: 'goal'
        };
        const emptyMap = () => ({
            goal_tree: { root_goal_id: null, goals: [] },
            root_candidates: [], work_items: [], unassigned_work: [], resources: [], memories: [], edges: []
        });
        const emptyPlan = () => ({ goal_tree: { root_goal_id: null, goals: [] }, components: [], decisions: [], warnings: [], validation: null });
        const arrayOf = value => Array.isArray(value) ? value : [];
        const objectOf = value => value && typeof value === 'object' ? value : {};
        const normalizeGoalTree = value => {
            const tree = objectOf(value);
            return { ...tree, root_goal_id: tree.root_goal_id ?? null, goals: arrayOf(tree.goals).filter(item => item && typeof item === 'object') };
        };
        const normalizeMap = value => {
            const map = objectOf(value);
            return {
                ...emptyMap(), ...map,
                goal_tree: normalizeGoalTree(map.goal_tree),
                root_candidates: arrayOf(map.root_candidates),
                work_items: arrayOf(map.work_items).filter(item => item && typeof item === 'object'),
                unassigned_work: arrayOf(map.unassigned_work).filter(item => item && typeof item === 'object'),
                resources: arrayOf(map.resources).filter(item => item && typeof item === 'object'),
                memories: arrayOf(map.memories).filter(item => item && typeof item === 'object'),
                edges: arrayOf(map.edges).filter(item => item && typeof item === 'object')
            };
        };
        const normalizeGraph = value => {
            const graph = objectOf(value);
            return { ...graph, nodes: arrayOf(graph.nodes).filter(item => item && typeof item === 'object'), edges: arrayOf(graph.edges).filter(item => item && typeof item === 'object') };
        };
        const normalizePlan = value => {
            const plan = objectOf(value);
            return {
                ...emptyPlan(), ...plan,
                goal_tree: normalizeGoalTree(plan.goal_tree),
                components: arrayOf(plan.components).filter(item => item && typeof item === 'object').map(component => ({ ...component, tasks: arrayOf(component.tasks).filter(task => task && typeof task === 'object') })),
                decisions: arrayOf(plan.decisions).filter(item => item && typeof item === 'object'),
                warnings: arrayOf(plan.warnings).filter(item => item && typeof item === 'object')
            };
        };
        const unwrap = value => {
            const result = api.toolValue(value);
            if (result && result.result_omitted) throw i18n.error('error.tooLarge');
            return result && typeof result === 'object' ? result : {};
        };
        const mapItemKey = (kind, item) => {
            if (!item || typeof item !== 'object') return `${kind}:0`;
            if (kind === 'goal') return `goal:${number(item.id)}`;
            if (kind === 'work') return `work:${number(item.id)}`;
            if (item.key && text(item.key).startsWith(`${kind}:`)) return text(item.key);
            const id = number(item.memory_id || item.resource_id || item.id);
            if (kind === 'memory') return `memory:${text(item.memory_type) || 'fact'}:${id}`;
            return `${kind}:${id || text(item.key)}`;
        };
        const itemTitle = (kind, item, t) => {
            if (!item) return '';
            if (kind === 'goal') return text(item.content) || t('item.goal', { id: String(number(item.id)) });
            if (kind === 'work') return text(item.title) || text(item.issue_key) || t('item.work', { id: String(number(item.id)) });
            if (kind === 'resource') return text(item.title) || text(item.name) || text(item.label) || text(item.repository) || text(item.external_id) || text(item.slug) || t('resource.linked');
            return text(item.content).split('\n')[0].replace(/^Summary:\s*/, '') || text(item.title) || t('item.memory', { type: t('memory.' + (item.memory_type || 'episode')), id: String(number(item.memory_id || item.id)) });
        };
        return {
            data() {
                const route = routeAPI.readRoute(window.location);
                api.token = '';
                api.requestId = 0;
                api.sessionId = '';
                return {
                    route,
                    locale: i18n.detectLocale(window),
                    rootSlug: text(route.project || route.namespace) || '/',
                    namespaces: [],
                    map: emptyMap(),
                    graph: { nodes: [], edges: [], total_nodes: 0, total_edges: 0 },
                    plan: emptyPlan(),
                    listItems: [],
                    listKind: '',
                    page: { hasMore: false, nextOffset: 0 },
                    contextError: '',
                    detailLoading: false, detailError: '', detailGeneration: 0,
                    maintenance: null, maintenanceAction: false, maintenanceNotice: '',
                    copyStatus: 'action.copyGuide',
                    mapLoaded: false,
                    fitMap: true,
                    mapAsList: true,
                    viewportWidth: 0,
                    selected: null,
                    filters: { query: route.query, status: route.status, agent: route.agent, memoryType: route.memoryType, issueType: route.issueType, label: route.label },
                    kindFilters: { goal: route.kinds.goal, work: route.kinds.work, memory: route.kinds.memory, resource: route.kinds.resource },
                    relations: { blocks: route.relations.blocks, part_of: route.relations.part_of, relates_to: route.relations.relates_to },
                    authLoading: true,
                    authChecked: false,
                    themePreference: document.documentElement.dataset.stashThemePreference || 'system',
                    loading: false,
                    error: '',
                    auth: { auth_mode: 'none', authenticated: false, user: '' },
                    authPanelOpen: false,
                    issuedToken: '',
                    tokenLoading: false,
                    tokenError: '',
                    apiAuthenticationExpired: null,
                    loadGeneration: 0
                };
            },
            computed: {
                navItems() { return navItems.map(item => ({ ...item, label: this.t(item.label) })); },
                kindOrder: () => ['goal', 'work', 'memory', 'resource'],
                kindNames() { return Object.fromEntries(Object.entries(kindNames).map(([kind, label]) => [kind, this.t(label)])); },
                activeKind() { const visible = this.kindOrder.filter(kind => this.kindFilters[kind]); return visible.length === 1 ? visible[0] : 'all'; },
                statusOptions() {
                    if (this.route.route === 'list_goals') return ['active', 'completed', 'abandoned'];
                    if (this.route.route === 'list_hypotheses' || (this.route.route === 'list_memories' && this.filters.memoryType === 'hypothesis')) return ['proposed', 'testing', 'confirmed', 'rejected'];
                    if (['list_memories', 'query_facts', 'list_namespaces', 'worktrees'].includes(this.route.route)) return [];
                    const work = ['backlog', 'ready', 'doing', 'blocked', 'review', 'done', 'canceled', 'expired'];
                    return work;
                },
                needsLogin() { return this.canLogin && !this.auth.authenticated; },
                hasFilters() { return Object.values(this.filters).some(Boolean) || Object.values(this.kindFilters).some(value => !value); },
                pageTitle() { return routeAPI.routeTitle(this.route.route, this.locale); },
                rootOptions() {
                    const values = [{ slug: '/', label: this.t('workspace.default') }];
                    for (const item of this.namespaces) {
                        if (!item.slug || item.slug === '/') continue;
                        values.push(item);
                    }
                    const seen = new Set();
                    return values.filter(item => !seen.has(item.slug) && seen.add(item.slug));
                },
                rootName() {
                    const match = this.rootOptions.find(item => item.slug === this.rootSlug);
                    return match ? text(match.name || match.label).replace(` · ${match.slug}`, '') : (this.rootSlug === '/' ? this.t('workspace.default') : this.rootSlug.split('/').pop());
                },
                canLogin() { return ['oauth', 'oidc', 'token'].includes(text(this.auth && this.auth.auth_mode)); },
                usesApiToken() { return Boolean(text(api.token)); },
                rootGoal() {
                    const tree = this.map.goal_tree || {};
                    return (tree.goals || []).find(goal => number(goal.id) === number(tree.root_goal_id)) || null;
                },
                planRootGoal() {
                    const tree = this.plan.goal_tree || {};
                    return (tree.goals || []).find(goal => number(goal.id) === number(tree.root_goal_id)) || null;
                },
                rootCounts() {
                    return {
                        goal: this.map.goal_tree.goals.length,
                        work: this.map.work_items.length + this.map.unassigned_work.length,
                        memory: this.map.memories.length,
                        resource: this.map.resources.length
                    };
                },
                allWork() {
                    const mapItems = [...(this.map.work_items || []), ...(this.map.unassigned_work || [])];
                    const graphItems = (this.graph.nodes || []).map(node => node && (node.item || node));
                    const planItems = (this.plan.components || []).flatMap(component => [component, ...component.tasks]);
                    const seen = new Map();
                    for (const item of [...mapItems, ...graphItems, ...planItems, ...(this.listKind === 'work' ? this.listItems : [])]) {
                        if (item && item.id != null) seen.set(number(item.id), { ...seen.get(number(item.id)), ...item });
                    }
                    return [...seen.values()];
                },
                agents() { return [...new Set(this.allWork.map(item => text(item.agent_id || item.owner)).filter(Boolean))].sort((a, b) => a.localeCompare(b, this.locale)); },
                filteredMap() {
                    return goalMap.filterGoalMap({ ...this.map, work_items: [...this.map.work_items, ...this.map.unassigned_work] }, { query: this.filters.query, status: this.filters.status, agent: this.filters.agent, memoryType: this.filters.memoryType, kinds: this.kindFilters });
                },
                mapLayout() { return goalMap.buildGoalMapLayout(this.filteredMap); },
                graphNodes() {
                    const query = this.filters.query;
                    return (this.graph.nodes || []).filter(node => {
                        const item = node && (node.item || node); const status = this.displayStatus(item);
                        const owner = text(item.agent_id || item.owner);
                        return search.matchesSearch(item, query) && (!this.filters.status || status === this.filters.status) && (!this.filters.agent || owner === this.filters.agent);
                    });
                },
                graphEdges() {
                    const keys = new Set(this.graphNodes.map(node => String(node.id != null ? node.id : node.key)));
                    const endpoint = (edge, side) => edge[`${side}_item_id`] != null ? edge[`${side}_item_id`] : edge[`${side}_id`] != null ? edge[`${side}_id`] : edge[side];
                    return (this.graph.edges || []).filter(edge => this.relations[edge.edge_type || edge.type || 'relates_to'] !== false && keys.has(String(endpoint(edge, 'from'))) && keys.has(String(endpoint(edge, 'to'))));
                },
                graphLayout() { return workGraph.buildWorkGraphLayout(this.graphNodes, this.graphEdges, { relations: this.relations, sourceNodeCount: this.graph.total_nodes || this.graphNodes.length }); },
                monitorRows() {
                    const rank = { expired: 0, blocked: 1, doing: 2, review: 3, ready: 4, backlog: 5, done: 6, canceled: 7 };
                    return this.allWork.filter(item => search.matchesSearch(item, this.filters.query) && (!this.filters.status || this.displayStatus(item) === this.filters.status) && (!this.filters.agent || text(item.agent_id || item.owner) === this.filters.agent)).sort((a, b) => (rank[this.displayStatus(a)] ?? 9) - (rank[this.displayStatus(b)] ?? 9) || number(b.priority) - number(a.priority) || number(a.id) - number(b.id));
                },
                boardItems() { return this.listItems.map(item => this.allWork.find(work => number(work.id) === number(item.id)) || item); },
                boardColumns() { return ['backlog', 'ready', 'doing', 'blocked', 'review', 'done', 'canceled', 'expired'].map(status => ({ status, items: this.boardItems.filter(item => this.displayStatus(item) === status) })).filter(column => column.items.length); },
                isListRoute() { return ['list_namespaces', 'list_memories', 'query_facts', 'list_hypotheses', 'list_goals', 'worktrees'].includes(this.route.route); },
                visibleListItems() { return ['list_namespaces', 'worktrees'].includes(this.route.route) ? this.listItems.filter(item => search.matchesSearch(item, this.filters.query)) : this.listItems; },
                listPlaceholder() { return this.route.route === 'list_namespaces' ? this.t('search.workspace') : this.route.route === 'list_goals' ? this.t('search.goal') : this.route.route === 'list_hypotheses' ? this.t('search.hypothesis') : this.route.route === 'worktrees' ? this.t('search.git') : this.route.route === 'list_memories' ? this.t('search.memory') : this.t('search.fact'); },
                selectedTitle() {
                    if (!this.selected) return '';
                    const { kind, item } = this.selected;
                    return kind === 'memory' ? this.t('item.memory', { type: this.memoryTypeLabel(item.memory_type), id: String(number(item.memory_id || item.id)) }) : this.itemTitle(kind, item);
                },
                selectedParent() { return this.selected ? this.parentLink(this.selected.kind, this.selected.item) : null; },
                selectedChildren() { return this.selected ? this.childLinks(this.selected.kind, this.selected.item) : []; },
                selectedConnections() {
                    if (!this.selected) return [];
                    const key = this.selected.key;
                    const edges = [...this.map.edges];
                    for (const work of this.allWork) {
                        if (work.goal_id) edges.push({ from: `work:${work.id}`, to: `goal:${work.goal_id}`, relation: 'contributes_to' });
                    }
                    const seen = new Set();
                    return edges.filter(edge => edge.from === key || edge.to === key).flatMap(edge => {
                        const other = edge.from === key ? edge.to : edge.from;
                        const found = this.findByFocus(other);
                        const connectionKey = `${other}:${edge.relation}`;
                        if (!found || seen.has(connectionKey)) return [];
                        seen.add(connectionKey);
                        return [{ ...found, key: connectionKey, label: edge.derived && edge.relation !== 'derived_from' ? this.t('relation.derived', { relation: this.relationLabel(edge.relation, edge.from === key) }) : this.relationLabel(edge.relation, edge.from === key) }];
                    });
                },
                selectedFields() {
                    if (!this.selected) return [];
                    const item = this.selected.item || {}; const fields = [];
                    if (this.selected.kind === 'goal') fields.push({ label: this.t('field.status'), value: this.statusLabel(item.status) }, { label: this.t('field.progress'), value: this.goalProgressLabel(item) }, { label: this.t('field.check'), value: item.completion_mismatch ? this.t('progress.mismatch') : item.ready_to_complete ? this.t('progress.ready') : '' });
                    if (this.selected.kind === 'work') fields.push({ label: this.t('field.workKey'), value: item.issue_key || '#' + item.id }, { label: this.t('field.status'), value: this.statusLabel(this.displayStatus(item)) }, { label: this.t('field.owner'), value: this.agentLabel(item) }, { label: this.t('field.nextAction'), value: item.next_action || this.t('empty.record') }, { label: this.t('field.latestResult'), value: item.latest_result || this.t('empty.record') });
                    if (this.selected.kind === 'work' && item.description) fields.push({ label: this.t('field.description'), value: item.description });
                    if (this.selected.kind === 'memory') fields.push({ label: this.t('field.status'), value: item.memory_type === 'hypothesis' ? this.statusLabel(item.status) : '' }, { label: this.t('field.content'), value: item.content || '' }, { label: this.t('field.reason'), value: item.reason || '' }, { label: this.t('field.lesson'), value: item.lesson || '' }, { label: this.t('field.verification'), value: item.verification_plan || '' }, { label: this.t('field.preview'), value: item.content_truncated ? this.t('view.memoryPreview') : '' });
                    if (this.selected.kind === 'resource' && item.worktree_path) fields.push({ label: this.t('field.repository'), value: item.repository }, { label: this.t('field.branch'), value: item.branch }, { label: this.t('field.folder'), value: item.worktree_path });
                    if (this.selected.kind === 'resource' && !item.worktree_path) fields.push({ label: this.t('field.source'), value: item.source || this.t('resource.linked') }, { label: this.t('field.authority'), value: item.authority === 'external' ? this.t('field.externalAuthority') : this.t('field.stashAuthority') }, { label: this.t('field.address'), value: item.uri || item.external_id || item.slug || '' }, { label: this.t('field.summary'), value: item.summary || item.description || '' });
                    return fields.filter(field => text(field.value));
                },
                agentGuide() { return this.t('agent.guide'); },
                staticTitle() { return this.route.route === 'agent' ? this.t('nav.agent') : this.route.route === 'maintenance' ? this.t('nav.maintenance') : this.t('empty.pageTitle'); },
                staticText() { return this.t('empty.pageText'); }
            },
            mounted() {
                window.addEventListener('popstate', this.handlePopState);
                window.addEventListener('resize', this.measureViewport);
                this.apiAuthenticationExpired = () => this.markAuthenticationExpired();
                api.markAuthenticationExpired = this.apiAuthenticationExpired;
                this.applyLocale();
                this.bootstrap();
            },
            updated() { this.measureViewport(); },
            beforeUnmount() {
                window.removeEventListener('popstate', this.handlePopState);
                window.removeEventListener('resize', this.measureViewport);
                if (api.markAuthenticationExpired === this.apiAuthenticationExpired) delete api.markAuthenticationExpired;
            },
            methods: {
                t(key, params) { return i18n.translate(this.locale, key, params); },
                formatNumber(value) { return new Intl.NumberFormat(this.locale).format(number(value)); },
                applyLocale() { document.documentElement.lang = this.locale; document.title = this.pageTitle + ' · Stash'; },
                changeLocale(value) {
                    if (!i18n.locales.includes(value)) return;
                    this.locale = value;
                    try { window.localStorage.setItem('stash.locale', value); } catch (_) {}
                    this.applyLocale();
                },
                showKind(kind) { for (const key of this.kindOrder) this.kindFilters[key] = kind === 'all' || key === kind; this.syncURL(); },
                componentProgressLabel(item) {
                    const value = item.execution_progress;
                    if (!value) return '';
                    const progress = this.t('progress.tasks', { count: value.total - value.canceled, done: value.done });
                    return value.canceled ? this.t('progress.canceled', { progress, count: value.canceled }) : progress;
                },
                async fetchGoalMap(namespace, generation, tool = 'get_goal_map', args = {}) {
                    for (let attempt = 0; attempt < 2; attempt++) {
                        const map = emptyMap(); let offset = 0, snapshot = '';
                        try {
                            for (;;) {
                                const page = unwrap(await api.invokeTool(tool, { namespace, include_done: true, paged: true, limit: 100, offset, snapshot, ...args }));
                                if (generation !== this.loadGeneration) return emptyMap();
                                if (!Array.isArray(page.items) || !page.snapshot) return normalizeMap(page);
                                if (snapshot && snapshot !== page.snapshot) throw new Error('goal_map_changed');
                                snapshot = page.snapshot; map.goal_tree.root_goal_id = page.root_goal_id || null; map.resource_total = page.resource_total;
                                const lists = { goal: map.goal_tree.goals, root_candidate: map.root_candidates, work: map.work_items, unassigned_work: map.unassigned_work, memory: map.memories, resource: map.resources, edge: map.edges };
                                for (const entry of page.items) { if (lists[entry.kind] && entry.value) lists[entry.kind].push(entry.value); }
                                if (!page.has_more) return normalizeMap(map);
                                if (page.next_offset <= offset) throw i18n.error('error.mapPage');
                                offset = page.next_offset;
                            }
                        } catch (error) {
                            if (!text(error.message).includes('goal_map_changed')) throw error;
                            if (attempt) throw i18n.error('error.mapChanged');
                        }
                    }
                },
                async loadMemoryDetail() {
                    if (!this.selected || this.selected.kind !== 'memory') return;
                    const { key, item } = this.selected; const namespace = this.rootSlug; const generation = ++this.detailGeneration;
                    this.detailLoading = true; this.detailError = '';
                    const current = () => generation === this.detailGeneration && namespace === this.rootSlug && this.selected && this.selected.key === key;
                    try {
                        const fields = ['content', ...(item.memory_type === 'failure' ? ['reason', 'lesson'] : item.memory_type === 'hypothesis' ? ['verification_plan'] : [])];
                        const original = {}; let snapshot = '';
                        for (const field of fields) {
                            let offset = 0; original[field] = '';
                            for (;;) {
                                const page = unwrap(await api.invokeTool('get_memory', { namespace, memory_type: item.memory_type, memory_id: number(item.memory_id || item.id), field, offset, limit: 1000, snapshot }));
                                if (!current()) return;
                                if (typeof page.content !== 'string' || !page.snapshot) throw i18n.error('error.original');
                                snapshot = page.snapshot; original[field] += page.content; original.status = page.status;
                                if (!page.has_more) break;
                                if (page.next_offset <= offset) throw i18n.error('error.originalPage');
                                offset = page.next_offset;
                            }
                        }
                        if (current()) this.selected.item = { ...item, ...original, content_truncated: false };
                        const context = await this.fetchGoalMap(namespace, this.loadGeneration, 'get_memory_context', { memory_type: item.memory_type, memory_id: number(item.memory_id || item.id) });
                        if (current()) {
                            const memories = new Map(this.map.memories.map(memory => [memory.key, memory]));
                            for (const memory of context.memories) memories.set(memory.key, { ...memory, ...memories.get(memory.key) });
                            this.map.memories = [...memories.values()];
                            const edges = new Map(this.map.edges.map(edge => [edge.key, edge]));
                            for (const edge of context.edges) edges.set(edge.key, edge);
                            this.map.edges = [...edges.values()];
                        }
                    } catch (error) { if (current()) this.detailError = i18n.errorMessage(error, 'error.original'); }
                    finally { if (current()) this.detailLoading = false; }
                },
                async runMaintenance(action) {
                    if (this.maintenanceAction || !this.maintenance || !['retry', 'reindex'].includes(action)) return;
                    if (action === 'reindex' && !window.confirm(this.t('maintenance.confirm'))) return;
                    this.maintenanceAction = true; this.maintenanceNotice = ''; this.error = '';
                    try {
                        const result = await api.adminRequest('/admin/maintenance/embeddings/' + action, { method: 'POST' });
                        this.maintenance = result.status;
                        this.maintenanceNotice = { key: 'maintenance.queued', params: { count: number(action === 'retry' ? result.woken : result.queued) } };
                    } catch (error) { this.error = i18n.errorMessage(error); }
                    finally { this.maintenanceAction = false; }
                },
                async copyAgentGuide() {
                    try { await navigator.clipboard.writeText(this.agentGuide); this.copyStatus = 'action.copied'; }
                    catch (_) { this.copyStatus = 'action.copyFallback'; }
                },
                statusLabel(value) { return this.t(statusNames[text(value)] || text(value) || 'status.unknown'); },
                kindLabel(value) { return this.t(kindNames[value] || value || 'kind.item'); },
                memoryTypeLabel(value) { return ({ fact: this.t('memory.fact'), episode: this.t('memory.episode'), hypothesis: this.t('memory.hypothesis'), failure: this.t('memory.failure') })[text(value)] || this.t('kind.memory'); },
                progress(value) { return `${Math.round(Math.max(0, Math.min(1, number(value))) * 100)}%`; },
                goalProgressLabel(item) {
                    if (number(item.subtree_work_total)) return this.t('progress.tasks', { count: item.subtree_work_total, done: number(item.subtree_work_done) });
                    if (number(item.child_goal_total)) return this.t('progress.children', { count: item.child_goal_total, done: number(item.child_goal_completed) });
                    return item.status === 'completed' ? this.t('progress.goalRecorded') : this.t('progress.noWork');
                },
                relationLabel(relation, outgoing) {
                    if (relation === 'derived_from') return outgoing ? this.t('relation.original') : this.t('relation.facts');
                    if (relation === 'blocks') return outgoing ? this.t('relation.blockedByThis') : this.t('relation.prerequisite');
                    if (relation === 'part_of') return outgoing ? this.t('relation.parent') : this.t('relation.children');
                    if (relation === 'contributes_to') return outgoing ? this.t('relation.goal') : this.t('relation.contributors');
                    return ({ context: this.t('relation.context'), constraint: this.t('relation.constraint'), decision: this.t('relation.decision'), evidence: this.t('relation.evidence'), result: this.t('relation.result'), failure: this.t('memory.failure'), relates_to: this.t('relation.related'), supersedes: this.t('relation.supersedes') })[relation] || this.t('relation.related');
                },
                displayStatus(item) {
                    const expiry = new Date(item && item.lease_expires_at || '');
                    if (item && item.execution_progress) return item.execution_progress.status;
                    return text(item && item.attempt_status) === 'active' && !Number.isNaN(expiry.getTime()) && expiry.getTime() <= Date.now() ? 'expired' : text(item && item.status);
                },
                agentLabel(item) { return text(item && (item.agent_id || item.owner)) || this.t('empty.owner'); },
                workNote(item) { if (item && item.execution_progress) return this.componentProgressLabel(item); return text(item && item.next_action) || text(item && item.latest_result) || text(item && item.description) || '';  },
                nodeKey(node) { return node && (node.item.issue_key || '#' + number(node.item.memory_id || node.item.id)); },
                nodeTitle(node) { return this.itemTitle(node.kind, node.item); },
                nodeAria(node) { return `${this.kindLabel(node.kind)}: ${this.nodeTitle(node)}. ${node.kind === 'work' ? this.statusLabel(this.displayStatus(node.item)) : ''}`; },
                mapNodeClasses(node) { return { ['is-' + node.kind]: true, 'is-root': Boolean(node.focus), 'is-selected': Boolean(this.selected && this.selected.key === node.key) }; },
                measureViewport() {
                    const viewport = document.querySelector('.stash-map-viewport, .stash-graph-viewport');
                    if (viewport) this.viewportWidth = Math.max(1, viewport.clientWidth - 20);
                },
                canvasStyle(layout) { return { width: `${layout.width}px`, height: `${layout.height}px`, zoom: this.fitMap && this.viewportWidth ? Math.min(1, this.viewportWidth / layout.width) : 1 }; },
                mapItemKey(kind, item) { return mapItemKey(kind, item); },
                itemTitle(kind, item) { return itemTitle(kind, item, (key, params) => this.t(key, params)); },
                parentLink(kind, item) {
                    if (!['goal', 'work'].includes(kind)) return null;
                    const parentID = number(item && item.parent_id);
                    if (!parentID) return null;
                    const source = kind === 'goal' ? (this.map.goal_tree && this.map.goal_tree.goals || []) : this.allWork;
                    const parent = source.find(candidate => number(candidate.id) === parentID);
                    return parent ? { kind, item: parent, key: mapItemKey(kind, parent) } : null;
                },
                childLinks(kind, item) {
                    if (!['goal', 'work'].includes(kind)) return [];
                    const id = number(item && item.id);
                    if (!id) return [];
                    const source = kind === 'goal' ? (this.map.goal_tree && this.map.goal_tree.goals || []) : this.allWork;
                    return source.filter(candidate => number(candidate.parent_id) === id).map(child => ({ kind, item: child, key: mapItemKey(kind, child) }));
                },
                navHref(route) { return routeAPI.buildRoute(route, { project: isProject(this.rootSlug) ? this.rootSlug : '', namespace: this.rootSlug }); },
                issueHref(item) { return routeAPI.buildRoute('board', { project: isProject(this.rootSlug) ? this.rootSlug : '', namespace: this.rootSlug, issueID: number(item && item.id), focus: `work:${number(item && item.id)}`, detail: false }); },
                routeState() {
                    return { project: isProject(this.rootSlug) ? this.rootSlug : '', namespace: this.rootSlug, query: this.filters.query, status: this.filters.status, agent: this.filters.agent, memoryType: this.filters.memoryType, issueType: this.filters.issueType, label: this.filters.label, kinds: this.kindFilters, relations: this.relations, focus: this.selected ? this.selected.key : this.route.focus, issueID: this.selected && this.selected.kind === 'work' ? number(this.selected.item.id) : this.route.issueID, detail: this.route.detail, offset: this.route.offset };
                },
                syncURL(push = false) {
                    const href = routeAPI.buildRoute(this.route.route, this.routeState());
                    const current = window.location.pathname + window.location.search;
                    if (href !== current) (push ? window.history.pushState : window.history.replaceState).call(window.history, {}, '', href);
                    this.route = routeAPI.readRoute(window.location);
                    document.title = this.pageTitle + ' · Stash';
                },
                syncFiltersFromRoute() {
                    this.filters.query = this.route.query; this.filters.status = this.route.status; this.filters.agent = this.route.agent; this.filters.memoryType = this.route.memoryType; this.filters.issueType = this.route.issueType; this.filters.label = this.route.label; this.kindFilters = { ...this.route.kinds }; this.relations = { ...this.route.relations };
                },
                async navigate(route) {
                    this.route = routeAPI.readRoute(this.navHref(route));
                    this.syncFiltersFromRoute();
                    this.selected = null;
                    this.syncURL(true);
                    await this.loadRoute();
                },
                handlePopState() {
                    this.route = routeAPI.readRoute(window.location);
                    this.rootSlug = text(this.route.project || this.route.namespace) || '/';
                    this.syncFiltersFromRoute();
                    this.loadRoute();
                },
                async changeRoot() {
                    this.rootSlug = text(this.rootSlug) || '/'; this.selected = null; this.route = { ...this.route, detail: false, focus: '', issueID: 0, offset: 0 }; this.syncURL(true); await this.loadRoute();
                },
                resetFilters() {
                    this.filters = { ...this.filters, query: '', status: '', agent: '', memoryType: '', issueType: '', label: '' }; this.kindFilters = { goal: true, work: true, memory: true, resource: true }; this.relations = { blocks: true, part_of: true, relates_to: true }; this.syncURL(); if (this.isListRoute || this.route.route === 'board') return this.searchList();
                },
                selectObject(kind, item) {
                    if (!item) return;
                    this.detailGeneration++; this.detailError = ''; this.detailLoading = false;
                    const linked = this.findByFocus(mapItemKey(kind, item));
                    item = { ...(linked && linked.item), ...item };
                    this.selected = { kind, item, key: mapItemKey(kind, item) };
                    this.route.detail = false;
                    this.syncURL();
                },
                selectMapNode(node) { this.selectObject(node.kind, node.item); },
                selectGraphNode(node) { this.selectObject('work', node.item || node); },
                selectListItem(item) { this.selectObject(this.listKind || (item.slug ? 'resource' : 'memory'), item); },
                clearSelection() { this.detailGeneration++; this.detailError = ''; this.detailLoading = false; this.selected = null; this.route = { ...this.route, detail: false, focus: '', issueID: 0 }; this.syncURL(); },
                openDetail() { if (!this.selected) return; this.route.detail = true; this.syncURL(true); return this.loadMemoryDetail(); },
                closeDetail() { this.route.detail = false; this.syncURL(true); },
                findByFocus(focus) {
                    if (!focus) return null;
                    const entries = [];
                    for (const goal of this.map.goal_tree && this.map.goal_tree.goals || []) entries.push({ kind: 'goal', item: goal });
                    for (const work of this.allWork) entries.push({ kind: 'work', item: work });
                    for (const memory of this.map.memories || []) entries.push({ kind: 'memory', item: memory });
                    for (const resource of this.map.resources || []) entries.push({ kind: 'resource', item: resource });
                    const listed = this.listItems.find(item => mapItemKey(this.listKind || 'memory', item) === focus);
                    const found = entries.find(entry => mapItemKey(entry.kind, entry.item) === focus || text(entry.item && entry.item.key) === focus || (focus === `work:${number(entry.item && entry.item.id)}` && entry.kind === 'work')) || null;
                    return listed ? { kind: this.listKind || 'memory', item: { ...(found && found.item), ...listed } } : found;
                },
                restoreFocus() {
                    const focus = this.route.focus || (this.route.issueID ? `work:${this.route.issueID}` : '');
                    let found = this.findByFocus(focus);
                    const memory = /^memory:(fact|episode|hypothesis|failure):(\d+)$/.exec(focus);
                    if (!found && memory && this.route.detail) found = { kind: 'memory', item: { memory_type: memory[1], memory_id: Number(memory[2]) } };
                    this.selected = found ? { ...found, key: mapItemKey(found.kind, found.item) } : null;
                    if (this.route.detail) this.loadMemoryDetail();
                },
                async fetchNamespaces() {
                    const items = []; let offset = 0;
                    for (;;) {
                        const result = unwrap(await api.invokeTool('list_namespaces', { limit: 101, offset }));
                        const page = api.pageSlice(result, 100, offset);
                        items.push(...page.items);
                        if (!page.hasMore) break;
                        if (page.nextOffset <= offset) throw i18n.error('error.workspacePage');
                        offset = page.nextOffset;
                    }
                    this.namespaces = items.map(item => { const slug = text(item.slug || item.path); const name = text(item.name || item.title); return { ...item, slug: slug || '/', name, label: name && name !== slug ? `${name} · ${slug}` : slug || '/' }; }).filter(item => item.slug);
                },
                async bootstrap() {
                    this.authLoading = true; this.error = '';
                    try {
                        const response = await window.fetch('/auth/status', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
                        if (!response.ok) throw i18n.error('error.authStatus');
                        this.auth = await response.json();
                        this.authChecked = true;
                        if (this.needsLogin) return;
                        try {
                            const returnTo = window.sessionStorage.getItem('stash.loginReturn');
                            window.sessionStorage.removeItem('stash.loginReturn');
                            if (window.location.pathname === '/' && returnTo && /^\/ui\/[a-z-]+(?:\?|$)/.test(returnTo)) {
                                this.route = routeAPI.readRoute(returnTo); this.rootSlug = this.route.project || this.route.namespace || '/';
                                this.syncFiltersFromRoute(); this.syncURL();
                            }
                        } catch (_) { /* Session storage may be unavailable. */ }
                        await this.fetchNamespaces();
                        await this.loadRoute();
                    } catch (error) { this.error = i18n.errorMessage(error, 'error.page'); }
                    finally { this.authLoading = false; }
                },
                async searchList() { this.route.offset = 0; this.clearSelection(); await this.loadRoute(); },
                async nextPage() { this.route.offset = this.page.nextOffset; this.clearSelection(); await this.loadRoute(true); },
                async loadRoute(append = false) {
                    if (this.needsLogin) return;
                    const generation = ++this.loadGeneration;
                    const route = { ...this.route }; const filters = { ...this.filters }; const namespace = this.rootSlug || '/';
                    this.loading = true; this.error = ''; this.contextError = ''; this.selected = null;
                    try {
                        let map = emptyMap(), graph = { nodes: [], edges: [] }, plan = emptyPlan(), listItems = [], listKind = '', page = { hasMore: false, nextOffset: 0 }, mapLoaded = false, contextError = '';
                        const mapRoute = ['goal-map', 'monitor'].includes(route.route);
                        if (mapRoute || ['plan', 'board', 'graph', ...Object.keys(dataTools)].includes(route.route)) {
                            try { map = await this.fetchGoalMap(namespace, generation); mapLoaded = true; }
                            catch (error) { if (mapRoute || error.status === 401) throw error; contextError = 'error.context'; }
                        }
                        if (route.route === 'maintenance') {
                            const maintenance = await api.adminRequest('/admin/maintenance/embeddings');
                            if (generation === this.loadGeneration) this.maintenance = maintenance;
                        } else if (route.route === 'graph') {
                            graph = normalizeGraph(unwrap(await api.invokeTool('get_work_graph', { project: isProject(namespace) ? namespace : undefined, namespaces: isProject(namespace) ? undefined : namespace, include_done: true, node_limit: 200, edge_limit: 400 })));
                        } else if (route.route === 'plan') {
                            plan = normalizePlan(unwrap(await api.invokeTool('get_work_plan', { namespace })));
                        } else if (route.route === 'list_namespaces') {
                            listKind = 'resource'; listItems = this.namespaces;
                        } else if (dataTools[route.route] || ['board', 'worktrees'].includes(route.route)) {
                            const memoryType = route.route === 'query_facts' ? 'fact' : route.route === 'list_hypotheses' ? 'hypothesis' : '';
                            const tool = memoryType ? 'list_memories' : route.route === 'board' ? 'list_work_items' : route.route === 'worktrees' ? 'list_worktrees' : route.route;
                            const args = { namespaces: namespace, limit: 101, offset: route.offset };
                            if (tool !== 'list_worktrees') args.q = filters.query;
                            if (['list_goals', 'list_hypotheses'].includes(tool) && filters.status) args.status = filters.status;
                            if (tool === 'list_memories') { delete args.namespaces; args.namespace = namespace; args.memory_type = memoryType || filters.memoryType; if (filters.status) args.status = filters.status; }
                            if (tool === 'list_work_items') { args.issue_type = filters.issueType; args.label = filters.label; }
                            const value = unwrap(await api.invokeTool(tool, args));
                            page = api.pageSlice(value, 100, route.offset);
                            listKind = dataTools[route.route] || (route.route === 'board' ? 'work' : 'resource');
                            listItems = page.items.map(item => ({ ...item, ...(route.route === 'query_facts' ? { memory_type: 'fact', content_truncated: item.content_truncated === true } : route.route === 'list_hypotheses' ? { memory_type: 'hypothesis', content_truncated: item.content_truncated === true } : {}) }));
                        }
                        if (generation !== this.loadGeneration) return;
                        if (append) listItems = [...this.listItems, ...listItems];
                        Object.assign(this, { map, graph, plan, listItems, listKind, page, mapLoaded, contextError });
                        this.restoreFocus();
                        document.title = this.pageTitle + ' · Stash';
                    } catch (error) {
                        if (generation === this.loadGeneration) this.error = route.route === 'maintenance' && [401, 403, 503].includes(error.status) ? (error.status === 503 ? 'error.adminUnavailable' : 'error.admin') : i18n.errorMessage(error, 'error.page');
                    } finally { if (generation === this.loadGeneration) this.loading = false; }
                },
                listItemKey(item) { return mapItemKey(this.listKind, item); },
                listItemTitle(item) {
                    if (this.route.route === 'list_namespaces') return item.name && item.name !== item.slug ? item.name : item.slug === '/' ? this.t('workspace.default') : item.slug;
                    return this.listKind === 'goal' ? this.itemTitle('goal', item) : this.listKind === 'resource' && item.slug ? (item.name || item.slug) : this.itemTitle(this.listKind, item);
                },
                listItemSummary(item) {
                    if (this.route.route === 'worktrees') return [item.branch, item.worktree_path].filter(Boolean).join(' · ');
                    if (this.route.route === 'list_namespaces') return item.name && item.name !== item.slug ? `${item.slug}${item.description ? ' · ' + item.description : ''}` : item.description || '';
                    return item.summary || item.description || item.verification_plan || item.value || item.property || item.uri || (this.listKind === 'resource' ? '' : item.slug) || '';
                },
                changeTheme(value) { this.themePreference = ['system', 'light', 'dark'].includes(value) ? value : 'system'; try { window.localStorage.setItem('stash.theme', this.themePreference); } catch (_) {} if (root.stashConsoleApplyTheme) root.stashConsoleApplyTheme(); },
                markAuthenticationExpired() {
                    api.token = '';
                    api.sessionId = '';
                    this.auth = { ...(this.auth || {}), authenticated: false, user: '' };
                    this.authPanelOpen = false;
                    this.issuedToken = '';
                    this.tokenError = 'error.session';
                },
                beginLogin() {
                    try { window.sessionStorage.setItem('stash.loginReturn', window.location.pathname + window.location.search); } catch (_) {}
                    window.location.assign('/auth/login');
                },
                async logout() {
                    api.token = '';
                    api.sessionId = '';
                    this.authPanelOpen = false;
                    this.issuedToken = '';
                    try { await fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' }); } finally { window.location.assign('/'); }
                },
                async issueToken() {
                    if (this.tokenLoading) return; this.tokenLoading = true; this.tokenError = '';
                    try { const response = await fetch('/auth/token', { method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' } }); const body = await response.json().catch(() => ({})); if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`); this.issuedToken = text(body.token); this.authPanelOpen = true; } catch (error) { this.tokenError = i18n.errorMessage(error, 'error.token'); } finally { this.tokenLoading = false; }
                },
                async copyIssuedToken() { if (!this.issuedToken || !navigator.clipboard) return; try { await navigator.clipboard.writeText(this.issuedToken); } catch (_) {} }
            }
        };
    }
    return { createViewModel };
}));
