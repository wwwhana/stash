(function (root, factory) {
    const api = typeof module === 'object' && module.exports
        ? factory(require('./route-state.js'))
        : factory(root.StashRouteState);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashRouteViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (routeState) {
    'use strict';

    if (!routeState) throw new Error('경로 상태 모듈을 불러오지 못했습니다.');

    function createRouteViewModel() {
        const routes = routeState;
        const defaultKinds = () => ({ goal: true, work: true, memory: true, resource: true });
        const defaultRelations = () => ({ part_of: true, blocks: true, relates_to: true });
        return {
            routeInitialized: false,
            routeRestoring: false,
            routeRestoreGeneration: 0,

            currentRouteName() {
                if (routes.routePaths[this.activeNav]) return this.activeNav;
                return ({ 'goal-map': 'goal-map', plan: 'plan', monitor: 'monitor', board: 'board', graph: 'graph', worktrees: 'worktrees', maintenance: 'maintenance', agent: 'agent' })[this.view] || 'goal-map';
            },

            routeState(route = this.currentRouteName()) {
                const state = {
                    namespace: this.mapNamespaceSlug,
                    project: this.planNamespaceSlug,
                    issueID: this.selectedIssue && this.selectedIssue.id
                };
                if (route === 'goal-map') {
                    Object.assign(state, {
                        query: this.goalMapFilters.query,
                        status: this.goalMapFilters.status,
                        agent: this.goalMapFilters.agent,
                        memoryType: this.goalMapFilters.memoryType,
                        kinds: this.goalMapFilters.kinds
                    });
                } else if (route === 'graph') {
                    Object.assign(state, {
                        project: this.graphProjectSlug,
                        query: this.graphFilter.query,
                        status: this.graphFilter.status,
                        agent: this.graphFilter.agent,
                        relations: this.graphFilter.relations,
                        focus: this.graphFocusedKey
                    });
                } else if (route === 'monitor') {
                    Object.assign(state, {
                        project: this.projectMonitorProjectSlug,
                        status: this.projectMonitorFilter.status,
                        agent: this.projectMonitorFilter.agent,
                        focus: this.projectMonitorSelectedID
                    });
                } else if (route === 'board') {
                    Object.assign(state, {
                        project: this.boardProjectSlug,
                        namespace: this.boardNamespaceSlug,
                        query: this.boardFilter.q,
                        issueType: this.boardFilter.issueType,
                        label: this.boardFilter.label,
                        offset: this.boardPage.offset
                    });
                } else if (route === 'worktrees') {
                    state.offset = this.worktreePage.offset;
                } else if (['list_namespaces', 'query_facts', 'list_hypotheses', 'list_goals'].includes(route)) {
                    state.offset = this.listPage.offset;
                }
                return state;
            },

            routeHref(route) {
                return routes.buildRoute(route, this.routeState(route));
            },

            syncRoute(replace = false) {
                if (!this.routeInitialized || this.routeRestoring) return;
                const route = this.currentRouteName();
                const href = this.routeHref(route);
                document.title = routes.routeTitle(route) + ' · Stash';
                if (href === window.location.pathname + window.location.search) return;
                window.history[replace ? 'replaceState' : 'pushState']({ stashRoute: route }, '', href);
            },

            applyRouteState(route) {
                if (route.route === 'goal-map' || route.route === 'graph') this.mapNamespaceSlug = route.namespace;
                if (route.route === 'plan') this.planNamespaceSlug = route.project;
                if (route.route === 'monitor') {
                    this.projectMonitorProjectSlug = route.project;
                    this.projectMonitorFilter = { status: route.status, agent: route.agent };
                    this.projectMonitorSelectedID = Number(route.focus) || 0;
                }
                if (route.route === 'goal-map') {
                    this.goalMapFilters = {
                        query: route.query,
                        status: route.status,
                        agent: route.agent,
                        memoryType: route.memoryType,
                        kinds: { ...defaultKinds(), ...route.kinds }
                    };
                }
                if (route.route === 'graph') {
                    this.graphProjectSlug = route.project;
                    this.graphFilter = {
                        query: route.query,
                        status: route.status,
                        agent: route.agent,
                        relations: { ...defaultRelations(), ...route.relations }
                    };
                    this.graphFocusedKey = route.focus;
                }
                if (route.route === 'board') {
                    this.boardProjectSlug = route.project;
                    this.boardNamespaceSlug = route.namespace;
                    this.boardFilter = { q: route.query, issueType: route.issueType, label: route.label };
                    this.boardPage.offset = route.offset;
                    this.boardPage.history = [];
                }
                if (route.route === 'worktrees') {
                    this.worktreePage.offset = route.offset;
                    this.worktreePage.history = [];
                }
                if (['query_facts', 'list_hypotheses', 'list_goals'].includes(route.route)) this.mapNamespaceSlug = route.namespace;
            },

            async restoreRoute() {
                const generation = ++this.routeRestoreGeneration;
                const route = routes.readRoute(window.location.href);
                this.routeRestoring = true;
                this.applyRouteState(route);
                try {
                    if (route.route === 'goal-map') await this.loadGoalMap();
                    else if (route.route === 'plan') await this.loadWorkPlan();
                    else if (route.route === 'monitor') await this.loadProjectMonitor();
                    else if (route.route === 'board') await this.loadWorkBoard(false);
                    else if (route.route === 'graph') await this.loadWorkGraph();
                    else if (route.route === 'worktrees') await this.loadWorktrees(false);
                    else if (route.route === 'agent') this.showAgentGuide();
                    else if (route.route === 'maintenance') await this.loadMaintenance();
                    else {
                        const args = route.route === 'list_namespaces' ? {} : { namespaces: route.namespace || '/' };
                        await this.callTool(route.route, args, route.offset);
                    }
                    if (generation !== this.routeRestoreGeneration) return;
                    if (['goal-map', 'monitor', 'board', 'graph'].includes(route.route)) {
                        if (route.issueID) await this.openIssue(route.issueID);
                        else if (this.selectedIssue) this.closeIssue();
                    }
                    if (generation !== this.routeRestoreGeneration) return;
                    if (route.route === 'graph' && route.focus) await this.focusGraphNodeByID(route.focus);
                } finally {
                    if (generation !== this.routeRestoreGeneration) return;
                    this.routeRestoring = false;
                    this.routeInitialized = true;
                    this.syncRoute(true);
                }
            }
        };
    }


    return { createRouteViewModel };
}));
