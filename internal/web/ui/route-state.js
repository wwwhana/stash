(function (root, factory) {
    const api = factory(typeof module === 'object' && module.exports ? require('./console-i18n.js') : root.StashI18n);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashRouteState = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (i18n) {
    'use strict';

    const routePaths = Object.freeze({
        'goal-map': '/ui/goal-map',
        plan: '/ui/plan',
        monitor: '/ui/monitor',
        board: '/ui/issues',
        graph: '/ui/work-graph',
        worktrees: '/ui/git',
        list_namespaces: '/ui/namespaces',
        list_memories: '/ui/memories',
        query_facts: '/ui/facts',
        list_hypotheses: '/ui/hypotheses',
        list_goals: '/ui/goals',
        agent: '/ui/agent-guide',
        maintenance: '/ui/maintenance'
    });
    const routeTitles = Object.freeze({
        'goal-map': 'nav.overview',
        plan: 'nav.plan',
        monitor: 'nav.monitor',
        board: 'nav.board',
        graph: 'nav.graph',
        worktrees: 'nav.git',
        list_namespaces: 'nav.workspaces',
        list_memories: 'nav.memories',
        query_facts: 'nav.facts',
        list_hypotheses: 'nav.hypotheses',
        list_goals: 'nav.goals',
        agent: 'nav.agent',
        maintenance: 'nav.maintenance'
    });
    const pathRoutes = new Map(Object.entries(routePaths).map(([route, path]) => [path, route]));
    const compatibilityPaths = new Set(['/ui/monitor-vue', '/ui/monitor-alpine']);
    const kinds = ['goal', 'work', 'memory', 'resource'];
    const relations = ['part_of', 'blocks', 'relates_to'];

    function text(value) {
        return String(value || '').trim();
    }

    function positiveInteger(value) {
        const number = Number(value);
        return Number.isInteger(number) && number > 0 ? number : 0;
    }

    function normalizedPath(value) {
        const path = String(value || '/').replace(/\/+$/, '') || '/';
        if (path === '/' || path === '/index.html') return '/';
        return path;
    }

    function readRoute(value) {
        const url = value instanceof URL ? value : new URL(String(value || '/'), 'http://stash.local');
        const path = normalizedPath(url.pathname);
        const route = pathRoutes.get(path) || (compatibilityPaths.has(path) ? 'monitor' : 'goal-map');
        const hidden = new Set(text(url.searchParams.get('hide')).split(',').map(item => item.trim()).filter(item => kinds.includes(item)));
        const hiddenRelations = new Set(text(url.searchParams.get('hide_relation')).split(',').map(item => item.trim()).filter(item => relations.includes(item)));
        return {
            route,
            matched: path === '/' || pathRoutes.has(path) || compatibilityPaths.has(path),
            namespace: text(url.searchParams.get('namespace')),
            project: text(url.searchParams.get('project')),
            query: text(url.searchParams.get('q')),
            status: text(url.searchParams.get('status')),
            agent: text(url.searchParams.get('agent')),
            memoryType: text(url.searchParams.get('memory')),
            kinds: Object.fromEntries(kinds.map(kind => [kind, !hidden.has(kind)])),
            relations: Object.fromEntries(relations.map(relation => [relation, !hiddenRelations.has(relation)])),
            focus: text(url.searchParams.get('focus')),
            detail: text(url.searchParams.get('detail')) === '1',
            issueType: text(url.searchParams.get('type')),
            label: text(url.searchParams.get('label')),
            offset: positiveInteger(url.searchParams.get('offset')),
            issueID: positiveInteger(url.searchParams.get('issue'))
        };
    }

    function setText(params, key, value) {
        const normalized = text(value);
        if (normalized) params.set(key, normalized);
    }

    function setNamespace(params, value) {
        const normalized = text(value);
        if (normalized && normalized !== '/') params.set('namespace', normalized);
    }

    function setOffset(params, value) {
        const offset = positiveInteger(value);
        if (offset) params.set('offset', String(offset));
    }

    function buildRoute(route, state) {
        const selected = Object.prototype.hasOwnProperty.call(routePaths, route) ? route : 'goal-map';
        const value = state && typeof state === 'object' ? state : {};
        const params = new URLSearchParams();
        if (selected === 'goal-map') {
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setText(params, 'status', value.status);
            setText(params, 'agent', value.agent);
            setText(params, 'memory', value.memoryType);
            const hidden = kinds.filter(kind => value.kinds && value.kinds[kind] === false);
            if (hidden.length) params.set('hide', hidden.join(','));
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (selected === 'graph') {
            setText(params, 'project', value.project);
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setText(params, 'status', value.status);
            setText(params, 'agent', value.agent);
            const hidden = relations.filter(relation => value.relations && value.relations[relation] === false);
            if (hidden.length) params.set('hide_relation', hidden.join(','));
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (selected === 'plan') {
            setText(params, 'project', value.project);
            if (!value.project) setNamespace(params, value.namespace);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (selected === 'monitor') {
            setText(params, 'project', value.project);
            if (!value.project) setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setText(params, 'status', value.status);
            setText(params, 'agent', value.agent);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (selected === 'board') {
            setText(params, 'project', value.project);
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setText(params, 'type', value.issueType);
            setText(params, 'label', value.label);
            setOffset(params, value.offset);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (selected === 'worktrees') {
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setOffset(params, value.offset);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        } else if (['list_memories', 'query_facts', 'list_hypotheses', 'list_goals'].includes(selected)) {
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setText(params, 'status', value.status);
            setOffset(params, value.offset);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
            if (selected === 'list_memories') setText(params, 'memory', value.memoryType);
        } else if (selected === 'list_namespaces') {
            setNamespace(params, value.namespace);
            setText(params, 'q', value.query);
            setOffset(params, value.offset);
            setText(params, 'focus', value.focus);
            if (value.detail) params.set('detail', '1');
        }
        if (['goal-map', 'monitor', 'graph', 'board'].includes(selected)) {
            const issueID = positiveInteger(value.issueID);
            if (issueID) params.set('issue', String(issueID));
        }
        const query = params.toString();
        return routePaths[selected] + (query ? '?' + query : '');
    }

    function routeTitle(route, locale = 'ko') {
        return i18n.translate(locale, routeTitles[route] || routeTitles['goal-map']);
    }

    return { routePaths, readRoute, buildRoute, routeTitle };
}));
