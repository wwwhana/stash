(function (root) {
    'use strict';

    const runtime = root.StashVueRuntime;
    const routeAPI = root.StashRouteState;
    const api = root.StashApiClient && root.StashApiClient.createApiClient();
    const goalMap = root.StashGoalMap;
    const workGraph = root.StashWorkGraph;
    const search = root.StashSearch;

    if (!runtime || !routeAPI || !api || !goalMap || !workGraph || !search) return;

    const { createApp } = runtime;
    const text = value => String(value == null ? '' : value).trim();
    const number = value => Number.isFinite(Number(value)) ? Number(value) : 0;
    const isProject = value => /^\/projects\/[^/]+$/.test(text(value));
    const statusNames = {
        backlog: '대기', ready: '준비', active: '진행 중', doing: '진행 중', blocked: '막힘',
        review: '검토', done: '완료', canceled: '취소', expired: '연결 만료'
    };
    const kindNames = { goal: '목표', work: '작업', memory: '기억', resource: '자료' };
    const navItems = [
        { route: 'goal-map', label: '목표·지식 지도', icon: '◎' },
        { route: 'plan', label: '작업 계획', icon: '◇' },
        { route: 'monitor', label: '작업 관제', icon: '◉' },
        { route: 'board', label: '이슈 보드', icon: '▦' },
        { route: 'graph', label: '작업 흐름', icon: '⌁' },
        { route: 'worktrees', label: 'Git 연결', icon: '⌘' }
    ];
    const dataTools = {
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
        return result && typeof result === 'object' ? result : {};
    };
    const itemsOf = value => {
        const result = unwrap(value);
        if (Array.isArray(result)) return result;
        for (const key of ['items', 'namespaces', 'goals', 'hypotheses', 'facts', 'worktrees']) {
            if (Array.isArray(result[key])) return result[key];
        }
        return [];
    };
    const mapItemKey = (kind, item) => {
        if (!item || typeof item !== 'object') return `${kind}:0`;
        if (kind === 'goal') return `goal:${number(item.id)}`;
        if (kind === 'work') return `work:${number(item.id)}`;
        if (item.key && text(item.key).startsWith(`${kind}:`)) return text(item.key);
        const id = number(item.memory_id || item.resource_id || item.id);
        return `${kind}:${id || text(item.key)}`;
    };
    const itemTitle = (kind, item) => {
        if (!item) return '';
        if (kind === 'goal') return text(item.content) || `목표 #${number(item.id)}`;
        if (kind === 'work') return text(item.title) || text(item.issue_key) || `작업 #${number(item.id)}`;
        if (kind === 'resource') return text(item.title) || text(item.name) || text(item.label) || text(item.external_id) || text(item.slug) || '연결 자료';
        return text(item.content) || text(item.title) || `${text(item.memory_type) || '기억'} #${number(item.memory_id || item.id)}`;
    };
    const safeJSON = value => JSON.stringify(value || {}, null, 2);

    const template = `
<div class="stash-console">
  <aside class="stash-sidebar" aria-label="주 메뉴">
    <div class="stash-brand"><span class="stash-brand-mark">S</span><span>Stash</span></div>
    <nav class="stash-nav">
      <span class="stash-nav-label">작업 공간</span>
      <a v-for="item in navItems" :key="item.route" :href="navHref(item.route)" :class="{'is-active': route.route === item.route}" @click.prevent="navigate(item.route)"><span class="stash-nav-icon">{{ item.icon }}</span><span>{{ item.label }}</span></a>
      <span class="stash-nav-label">자료</span>
      <a href="/ui/namespaces" :class="{'is-active': route.route === 'list_namespaces'}" @click.prevent="navigate('list_namespaces')"><span class="stash-nav-icon">◌</span><span>네임스페이스</span></a>
      <a href="/ui/facts" :class="{'is-active': route.route === 'query_facts'}" @click.prevent="navigate('query_facts')"><span class="stash-nav-icon">✓</span><span>사실</span></a>
      <a href="/ui/hypotheses" :class="{'is-active': route.route === 'list_hypotheses'}" @click.prevent="navigate('list_hypotheses')"><span class="stash-nav-icon">?</span><span>가설</span></a>
      <a href="/ui/goals" :class="{'is-active': route.route === 'list_goals'}" @click.prevent="navigate('list_goals')"><span class="stash-nav-icon">↗</span><span>목표</span></a>
    </nav>
    <div class="stash-sidebar-foot"><a href="/ui/agent-guide" @click.prevent="navigate('agent')">⌘ 에이전트 규칙</a><a href="/ui/maintenance" @click.prevent="navigate('maintenance')">◌ 임베딩 관리</a></div>
  </aside>

  <main class="stash-main">
    <header class="stash-topbar">
      <div><h1>{{ pageTitle }}</h1></div>
      <div class="stash-top-actions">
        <label class="stash-root-select"><span>루트 객체</span><select v-model="rootSlug" @change="changeRoot"><option v-for="item in rootOptions" :key="item.slug" :value="item.slug">{{ item.label }}</option></select></label>
        <a v-if="canLogin && !auth.authenticated" class="stash-button is-primary" href="/auth/login">로그인</a>
        <button v-else-if="auth.authenticated" type="button" class="stash-button" @click="authPanelOpen = !authPanelOpen">{{ auth.user || '로그인됨' }}</button>
        <select class="stash-theme-select" aria-label="테마" :value="themePreference" @change="changeTheme($event.target.value)"><option value="system">시스템</option><option value="light">밝게</option><option value="dark">어둡게</option></select>
      </div>
    </header>

    <section v-if="authPanelOpen" class="stash-token-panel">
      <h3>{{ auth.user || '로그인됨' }}</h3>
      <p>{{ usesApiToken ? '저장한 API 토큰으로 연결되어 있습니다.' : '현재 브라우저 세션으로 연결되어 있습니다.' }}</p>
      <div class="stash-token-actions"><button type="button" class="stash-button" :disabled="tokenLoading" @click="issueToken">{{ tokenLoading ? '발급 중…' : '새 토큰 발급' }}</button><a class="stash-button is-quiet" href="/auth/logout" @click.prevent="logout">로그아웃</a></div>
      <div class="stash-token-issued" v-if="issuedToken"><code>{{ issuedToken }}</code><button type="button" class="stash-button" @click="copyIssuedToken">토큰 복사</button></div>
      <div v-if="tokenError" class="stash-error">{{ tokenError }}</div>
    </section>

    <section class="stash-surface">
      <div class="stash-root-summary">
        <div><p class="stash-kicker">현재 범위</p><h2>{{ rootName }}</h2><p>{{ rootSlug }}</p></div>
        <div class="stash-counts" aria-label="루트 요약"><div class="stash-count"><strong>{{ rootCounts.goal }}</strong><span>목표</span></div><div class="stash-count"><strong>{{ rootCounts.work }}</strong><span>작업</span></div><div class="stash-count"><strong>{{ rootCounts.memory }}</strong><span>기억</span></div><div class="stash-count"><strong>{{ rootCounts.resource }}</strong><span>자료</span></div></div>
      </div>
      <div v-if="error" class="stash-error" role="alert">{{ error }} <a v-if="canLogin && !auth.authenticated" href="/auth/login">로그인</a></div>
      <div v-if="loading" class="stash-loading">불러오는 중…</div>
      <div v-else class="stash-content-grid" :class="{'is-detail': route.detail}">
        <section>
          <article v-if="route.detail && selected" class="stash-detail">
            <header><div><p class="stash-kicker">{{ kindLabel(selected.kind) }} 상세</p><h2>{{ selectedTitle }}</h2></div><button type="button" class="stash-button" @click="closeDetail">목록으로</button></header>
            <dl><div v-for="field in selectedFields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div></dl>
            <div v-if="selectedParent || selectedChildren.length" class="stash-related-links"><div v-if="selectedParent"><span>상위 {{ kindLabel(selectedParent.kind) }}</span><button type="button" @click="selectObject(selectedParent.kind, selectedParent.item)">{{ itemTitle(selectedParent.kind, selectedParent.item) }}</button></div><div v-if="selectedChildren.length"><span>하위 {{ selectedChildren.length }}개</span><div><button v-for="child in selectedChildren" :key="child.key" type="button" @click="selectObject(child.kind, child.item)">{{ itemTitle(child.kind, child.item) }}</button></div></div></div>
            <pre>{{ pretty(selected.item) }}</pre>
          </article>

          <template v-else-if="route.route === 'goal-map'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>검색</span><input v-model="filters.query" placeholder="목표, 작업, 사실, 자료" @input="syncURL"></label><label class="stash-field"><span>상태</span><select v-model="filters.status" @change="syncURL"><option value="">모든 상태</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><label class="stash-field"><span>담당</span><select v-model="filters.agent" @change="syncURL"><option value="">모든 담당</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label><label class="stash-field"><span>기억 유형</span><select v-model="filters.memoryType" @change="syncURL"><option value="">모든 기억</option><option value="fact">사실</option><option value="episode">경험</option><option value="hypothesis">가설</option><option value="failure">실패 기록</option></select></label><button type="button" class="stash-button" @click="resetFilters">초기화</button></div>
            <div class="stash-checks"><span>표시</span><label v-for="kind in kindOrder" :key="kind"><input type="checkbox" v-model="kindFilters[kind]" @change="syncURL">{{ kindNames[kind] }}</label></div>
            <div class="stash-filter-chips"><span v-if="filters.query" class="stash-chip">검색: {{ filters.query }} <button type="button" @click="filters.query='';syncURL()">×</button></span><span v-if="filters.status" class="stash-chip">상태: {{ statusLabel(filters.status) }} <button type="button" @click="filters.status='';syncURL()">×</button></span><span v-if="filters.agent" class="stash-chip">담당: {{ filters.agent }} <button type="button" @click="filters.agent='';syncURL()">×</button></span></div>
            <div class="stash-legend"><span><i></i>목표 연결</span><span><i class="is-dash"></i>자료·기억</span><span><i style="color:#fb7185"></i>막는 관계</span></div>
            <div v-if="!mapLayout.nodes.length" class="stash-empty"><strong>연결된 객체가 없습니다.</strong><span>이 루트에 목표나 작업을 추가하세요.</span></div>
            <div v-else class="stash-map-viewport"><div class="stash-map-canvas" :style="canvasStyle(mapLayout)"><svg class="stash-map-edge-layer" :viewBox="'0 0 ' + mapLayout.width + ' ' + mapLayout.height" aria-hidden="true"><defs><marker id="stash-map-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#818cf8"></path></marker></defs><path v-for="edge in mapLayout.edges" :key="edge.key" :d="edge.path" :stroke="edge.stroke" stroke-width="2" :stroke-dasharray="edge.dashArray || null" :marker-end="edge.marker ? 'url(#stash-map-arrow)' : null" fill="none"></path></svg><div v-for="ring in mapLayout.rings" :key="ring.key" class="stash-map-ring" :style="ring.style"><span>{{ ring.label }} · {{ ring.count }}</span></div><button v-for="node in mapLayout.nodes" :key="node.key" type="button" class="stash-map-node" :class="mapNodeClasses(node)" :style="node.style" :aria-label="nodeAria(node)" @click="selectMapNode(node)"><span class="stash-node-meta"><span class="stash-node-key">{{ nodeKey(node) }}</span><span v-if="node.kind === 'work'" class="stash-status" :data-status="displayStatus(node.item)">{{ statusLabel(displayStatus(node.item)) }}</span><span v-else>{{ kindLabel(node.kind) }}</span></span><span class="stash-node-title">{{ nodeTitle(node) }}</span><span v-if="node.kind === 'work'" class="stash-node-note">{{ workNote(node.item) }}</span><span v-else-if="node.kind === 'resource'" class="stash-node-note">{{ node.item.source || '연결 자료' }}</span><span v-else-if="node.kind === 'memory'" class="stash-node-note">{{ memoryTypeLabel(node.item.memory_type) }}</span></button></div></div>
          </template>

          <template v-else-if="route.route === 'graph'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>검색</span><input v-model="filters.query" placeholder="작업, 담당, 결과" @input="syncURL"></label><label class="stash-field"><span>상태</span><select v-model="filters.status" @change="syncURL"><option value="">모든 상태</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><label class="stash-field"><span>담당</span><select v-model="filters.agent" @change="syncURL"><option value="">모든 담당</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label><button type="button" class="stash-button" @click="resetFilters">초기화</button></div><div class="stash-checks"><span>관계</span><label><input type="checkbox" v-model="relations.blocks" @change="syncURL">선행</label><label><input type="checkbox" v-model="relations.part_of" @change="syncURL">포함</label><label><input type="checkbox" v-model="relations.relates_to" @change="syncURL">관련</label></div><div class="stash-legend"><span><i style="color:#e9a23b"></i>먼저 끝나야 함</span><span><i style="color:#9b8cf2"></i>상위 작업에 포함</span><span><i class="is-dash"></i>관련</span></div>
            <div v-if="!graphLayout.nodes.length" class="stash-empty"><strong>연결된 작업이 없습니다.</strong><span>작업 관계가 생기면 이곳에 표시됩니다.</span></div>
            <div v-else class="stash-graph-viewport"><div class="stash-graph-canvas" :style="canvasStyle(graphLayout)"><svg class="stash-graph-edge-layer" :viewBox="'0 0 ' + graphLayout.width + ' ' + graphLayout.height" aria-hidden="true"><defs><marker id="stash-graph-arrow" markerWidth="8" height="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#e9a23b"></path></marker></defs><path v-for="edge in graphLayout.edges" :key="edge.key" :d="edge.path" :stroke="edge.stroke" stroke-width="2" :stroke-dasharray="edge.dashArray || null" :marker-end="edge.marker ? 'url(#stash-graph-arrow)' : null" fill="none"></path></svg><button v-for="node in graphLayout.nodes" :key="node.key" type="button" class="stash-graph-node" :class="{'is-selected': selected && selected.key === node.key}" :style="node.style" @click="selectGraphNode(node)"><span class="stash-node-meta"><span class="stash-node-key">{{ node.item.issue_key || '#' + node.item.id }}</span><span class="stash-status" :data-status="displayStatus(node.item)">{{ statusLabel(displayStatus(node.item)) }}</span></span><span class="stash-node-title">{{ node.item.title }}</span><span class="stash-node-note">{{ workNote(node.item) }}</span></button></div></div>
          </template>

          <template v-else-if="route.route === 'monitor'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>검색</span><input v-model="filters.query" placeholder="작업, 결과, 다음 행동" @input="syncURL"></label><label class="stash-field"><span>상태</span><select v-model="filters.status" @change="syncURL"><option value="">모든 상태</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><label class="stash-field"><span>담당</span><select v-model="filters.agent" @change="syncURL"><option value="">모든 담당</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label><button type="button" class="stash-button" @click="resetFilters">초기화</button></div><div class="stash-list"><button v-for="item in monitorRows" :key="item.id" type="button" class="stash-list-item" :class="{'is-selected': selected && selected.key === mapItemKey('work', item)}" @click="selectObject('work', item)"><span><strong>{{ item.issue_key || '#' + item.id }} · {{ item.title }}</strong><small>{{ workNote(item) }}</small></span><span class="stash-list-meta"><span class="stash-status" :data-status="displayStatus(item)">{{ statusLabel(displayStatus(item)) }}</span><span>{{ agentLabel(item) }}</span></span></button><div v-if="!monitorRows.length" class="stash-empty"><strong>표시할 작업이 없습니다.</strong></div></div>
          </template>

          <template v-else-if="route.route === 'board'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>검색</span><input v-model="filters.query" placeholder="작업 검색" @input="syncURL"></label><label class="stash-field"><span>유형</span><select v-model="filters.issueType" @change="syncURL"><option value="">모든 유형</option><option value="task">작업</option><option value="bug">버그</option><option value="feature">기능</option><option value="chore">정리</option><option value="question">질문</option></select></label><label class="stash-field"><span>라벨</span><input v-model="filters.label" placeholder="라벨" @input="syncURL"></label><button type="button" class="stash-button" @click="resetFilters">초기화</button></div><div class="stash-plan-grid"><div v-for="column in boardColumns" :key="column.status" class="stash-plan-component"><header><h3>{{ statusLabel(column.status) }}</h3><span class="stash-status" :data-status="column.status">{{ column.items.length }}</span></header><ul class="stash-plan-tasks"><li v-for="item in column.items" :key="item.id" class="stash-plan-task"><button type="button" @click="selectObject('work', item)"><strong>{{ item.issue_key || '#' + item.id }}</strong> {{ item.title }}</button><small>{{ agentLabel(item) }}</small></li><li v-if="!column.items.length" class="stash-plan-task"><small>없음</small></li></ul></div></div>
          </template>

          <template v-else-if="route.route === 'plan'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>작업자</span><input v-model="planActor" placeholder="작업자 이름" @input="syncURL"></label><button type="button" class="stash-button" @click="loadRoute">새로고침</button></div><div v-if="planRootGoal" class="stash-plan-goal"><span>공통 목표</span><strong>{{ planRootGoal.content }}</strong><span class="stash-status">{{ progress(planRootGoal.progress) }}</span></div><div v-if="!plan.components.length" class="stash-empty"><strong>등록된 구성 요소가 없습니다.</strong></div><div v-else class="stash-plan-grid"><article v-for="component in plan.components" :key="component.id" class="stash-plan-component"><header><div><h3>{{ component.issue_key || '#' + component.id }} · {{ component.title }}</h3><p>{{ component.description || '완료 조건이 없습니다.' }}</p></div><span class="stash-status" :data-status="component.status">{{ statusLabel(component.status) }}</span></header><ul class="stash-plan-tasks"><li v-for="task in component.tasks || []" :key="task.id" class="stash-plan-task"><button type="button" @click="selectObject('work', task)"><strong>{{ task.issue_key || '#' + task.id }}</strong> {{ task.title }}</button><small>{{ statusLabel(task.status) }}</small></li><li v-if="!(component.tasks || []).length" class="stash-plan-task"><small>하위 작업 없음</small></li></ul></article></div><div v-if="plan.decisions.length" class="stash-plan-decisions"><div v-for="decision in plan.decisions" :key="decision.id" class="stash-plan-decision"><strong>{{ decision.title }}</strong><div>{{ decision.rationale }}</div></div></div>
          </template>

          <template v-else-if="isListRoute">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>검색</span><input v-model="filters.query" :placeholder="listPlaceholder" @input="syncURL"></label><label v-if="route.route !== 'list_namespaces'" class="stash-field"><span>상태</span><select v-model="filters.status" @change="syncURL"><option value="">모든 상태</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><button type="button" class="stash-button" @click="loadRoute">검색</button></div><div class="stash-list"><button v-for="item in listItems" :key="listItemKey(item)" type="button" class="stash-list-item" :class="{'is-selected': selected && selected.key === listItemKey(item)}" @click="selectListItem(item)"><span><strong>{{ listItemTitle(item) }}</strong><small>{{ listItemSummary(item) }}</small></span><span class="stash-list-meta"><span v-if="item.status" class="stash-status" :data-status="item.status">{{ statusLabel(item.status) }}</span><span v-if="item.slug && route.route !== 'list_namespaces'">{{ item.slug }}</span></span></button><div v-if="!listItems.length" class="stash-empty"><strong>표시할 항목이 없습니다.</strong></div></div>
          </template>

          <template v-else><div class="stash-empty"><strong>{{ staticTitle }}</strong><span>{{ staticText }}</span></div></template>
        </section>

        <aside v-if="selected && !route.detail" class="stash-inspector" aria-label="선택한 객체">
          <div class="stash-inspector-head"><div><p class="stash-kicker">{{ kindLabel(selected.kind) }}</p><h3>{{ selectedTitle }}</h3></div><button type="button" aria-label="선택 해제" @click="clearSelection">×</button></div>
          <dl><div v-for="field in selectedFields.slice(0, 6)" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div></dl>
          <div v-if="selectedParent || selectedChildren.length" class="stash-related-links"><div v-if="selectedParent"><span>상위 {{ kindLabel(selectedParent.kind) }}</span><button type="button" @click="selectObject(selectedParent.kind, selectedParent.item)">{{ itemTitle(selectedParent.kind, selectedParent.item) }}</button></div><div v-if="selectedChildren.length"><span>하위 {{ selectedChildren.length }}개</span><div><button v-for="child in selectedChildren" :key="child.key" type="button" @click="selectObject(child.kind, child.item)">{{ itemTitle(child.kind, child.item) }}</button></div></div></div>
          <div class="stash-inspector-actions"><button type="button" class="stash-button is-primary" @click="openDetail">전체 보기</button><a v-if="selected.kind === 'work'" class="stash-button" :href="issueHref(selected.item)">작업 주소 열기</a></div>
        </aside>
      </div>
    </section>
  </main>
</div>`;

    const app = createApp({
        template,
        data() {
            const route = routeAPI.readRoute(window.location);
            const token = (() => { try { return sessionStorage.getItem('stash.apiToken') || ''; } catch (_) { return ''; } })();
            api.token = token;
            api.requestId = 0;
            api.sessionId = '';
            return {
                route,
                rootSlug: text(route.project || route.namespace) || '/',
                namespaces: [],
                map: emptyMap(),
                graph: { nodes: [], edges: [], total_nodes: 0, total_edges: 0 },
                plan: emptyPlan(),
                listItems: [],
                listKind: '',
                selected: null,
                filters: { query: route.query, status: route.status, agent: route.agent, memoryType: route.memoryType, issueType: route.issueType, label: '' },
                kindFilters: { goal: route.kinds.goal, work: route.kinds.work, memory: route.kinds.memory, resource: route.kinds.resource },
                relations: { blocks: route.relations.blocks, part_of: route.relations.part_of, relates_to: route.relations.relates_to },
                planActor: '',
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
            navItems: () => navItems,
            kindOrder: () => ['goal', 'work', 'memory', 'resource'],
            kindNames: () => kindNames,
            statusOptions: () => ['backlog', 'ready', 'doing', 'blocked', 'review', 'done', 'canceled'],
            pageTitle() { return routeAPI.routeTitle(this.route.route); },
            rootOptions() {
                const values = [{ slug: '/', label: '기본 공간 · /' }];
                for (const item of this.namespaces) {
                    if (!item.slug || item.slug === '/') continue;
                    values.push(item);
                }
                const seen = new Set();
                return values.filter(item => !seen.has(item.slug) && seen.add(item.slug));
            },
            rootName() {
                const match = this.rootOptions.find(item => item.slug === this.rootSlug);
                return match ? text(match.name || match.label).replace(` · ${match.slug}`, '') : (this.rootSlug === '/' ? '기본 공간' : this.rootSlug.split('/').pop());
            },
            themePreference() { return document.documentElement.dataset.stashThemePreference || 'system'; },
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
                const goals = (this.map.goal_tree && this.map.goal_tree.goals) || [];
                const planGoals = (this.plan.goal_tree && this.plan.goal_tree.goals) || [];
                const planTasks = (this.plan.components || []).reduce((total, component) => total + (component.tasks || []).length, 0);
                const listCount = this.listItems.length;
                return {
                    goal: goals.length || planGoals.length || (this.listKind === 'goal' ? listCount : 0),
                    work: this.allWork.length || planTasks,
                    memory: (this.map.memories || []).length || (this.listKind === 'memory' ? listCount : 0),
                    resource: (this.map.resources || []).length || (this.listKind === 'resource' ? listCount : 0)
                };
            },
            allWork() {
                const mapItems = [...(this.map.work_items || []), ...(this.map.unassigned_work || [])];
                const graphItems = (this.graph.nodes || []).map(node => node && (node.item || node));
                const planItems = (this.plan.components || []).flatMap(component => component.tasks || []);
                const seen = new Set();
                return [...mapItems, ...graphItems, ...planItems].filter(item => item && item.id != null && !seen.has(item.id) && seen.add(item.id));
            },
            agents() { return [...new Set(this.allWork.map(item => text(item.agent_id || item.owner)).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'ko')); },
            filteredMap() {
                return goalMap.filterGoalMap(this.map, { query: this.filters.query, status: this.filters.status, agent: this.filters.agent, memoryType: this.filters.memoryType, kinds: this.kindFilters });
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
            boardItems() { return this.monitorRows.filter(item => !this.filters.issueType || text(item.issue_type) === this.filters.issueType).filter(item => !this.filters.label || (Array.isArray(item.labels) ? item.labels : [item.labels]).map(text).includes(text(this.filters.label))); },
            boardColumns() { return ['backlog', 'ready', 'doing', 'blocked', 'review', 'done'].map(status => ({ status, items: this.boardItems.filter(item => this.displayStatus(item) === status) })); },
            isListRoute() { return ['list_namespaces', 'query_facts', 'list_hypotheses', 'list_goals'].includes(this.route.route); },
            listPlaceholder() { return this.route.route === 'list_namespaces' ? '네임스페이스 이름' : this.route.route === 'list_goals' ? '목표 내용' : this.route.route === 'list_hypotheses' ? '가설 내용' : '사실 내용'; },
            selectedTitle() { return this.selected ? itemTitle(this.selected.kind, this.selected.item) : ''; },
            selectedParent() { return this.selected ? this.parentLink(this.selected.kind, this.selected.item) : null; },
            selectedChildren() { return this.selected ? this.childLinks(this.selected.kind, this.selected.item) : []; },
            selectedFields() {
                if (!this.selected) return [];
                const item = this.selected.item || {}; const fields = [];
                if (this.selected.kind === 'goal') fields.push({ label: '상태', value: this.statusLabel(item.status) }, { label: '진척', value: this.progress(item.progress) }, { label: '상위 목표', value: item.parent_id ? '#' + item.parent_id : '없음' });
                if (this.selected.kind === 'work') fields.push({ label: '작업 키', value: item.issue_key || '#' + item.id }, { label: '상태', value: this.statusLabel(this.displayStatus(item)) }, { label: '담당', value: this.agentLabel(item) }, { label: '다음 행동', value: item.next_action || '기록 없음' }, { label: '최근 결과', value: item.latest_result || '기록 없음' });
                if (this.selected.kind === 'memory') fields.push({ label: '유형', value: this.memoryTypeLabel(item.memory_type) }, { label: '상태', value: item.status || '기록됨' }, { label: '출처', value: item.source || 'Stash' }, { label: '내용', value: item.content || '' });
                if (this.selected.kind === 'resource') fields.push({ label: '출처', value: item.source || '연결 자료' }, { label: '기준', value: item.authority === 'external' ? '외부 기준' : 'Stash 기준' }, { label: '주소', value: item.uri || item.external_id || item.slug || '' }, { label: '요약', value: item.summary || item.description || '' });
                return fields.filter(field => text(field.value));
            },
            staticTitle() { return this.route.route === 'agent' ? '에이전트 규칙' : this.route.route === 'maintenance' ? '임베딩 관리' : '준비된 화면이 없습니다.'; },
            staticText() { return this.route.route === 'agent' ? '작업 계획과 목표 범위를 먼저 확인하세요.' : this.route.route === 'maintenance' ? '벡터가 없는 기억은 원문 검색으로 계속 찾을 수 있습니다.' : ''; }
        },
        mounted() {
            window.addEventListener('popstate', this.handlePopState);
            this.apiAuthenticationExpired = () => this.markAuthenticationExpired();
            api.markAuthenticationExpired = this.apiAuthenticationExpired;
            this.bootstrap();
        },
        beforeUnmount() {
            window.removeEventListener('popstate', this.handlePopState);
            if (api.markAuthenticationExpired === this.apiAuthenticationExpired) delete api.markAuthenticationExpired;
        },
        methods: {
            statusLabel(value) { return statusNames[text(value)] || text(value) || '상태 없음'; },
            kindLabel(value) { return kindNames[value] || value || '객체'; },
            memoryTypeLabel(value) { return ({ fact: '사실', episode: '경험', hypothesis: '가설', failure: '실패 기록' })[text(value)] || '기억'; },
            progress(value) { return `${Math.round(Math.max(0, Math.min(1, number(value))) * 100)}%`; },
            displayStatus(item) {
                const expiry = new Date(item && item.lease_expires_at || '');
                return text(item && item.attempt_status) === 'active' && !Number.isNaN(expiry.getTime()) && expiry.getTime() <= Date.now() ? 'expired' : text(item && item.status);
            },
            agentLabel(item) { return text(item && (item.agent_id || item.owner)) || '담당 없음'; },
            workNote(item) { return text(item && item.next_action) || text(item && item.latest_result) || text(item && item.description) || '내용 없음'; },
            nodeKey(node) { return node && (node.key || mapItemKey(node.kind, node.item)); },
            nodeTitle(node) { return itemTitle(node.kind, node.item); },
            nodeAria(node) { return `${this.kindLabel(node.kind)}: ${this.nodeTitle(node)}. ${node.kind === 'work' ? this.statusLabel(this.displayStatus(node.item)) : ''}`; },
            mapNodeClasses(node) { return { ['is-' + node.kind]: true, 'is-root': Boolean(node.focus), 'is-selected': Boolean(this.selected && this.selected.key === node.key) }; },
            canvasStyle(layout) { return { width: `${layout.width}px`, height: `${layout.height}px` }; },
            pretty(value) { return safeJSON(value); },
            mapItemKey(kind, item) { return mapItemKey(kind, item); },
            itemTitle(kind, item) { return itemTitle(kind, item); },
            parentLink(kind, item) {
                const parentID = number(item && item.parent_id);
                if (!parentID) return null;
                const source = kind === 'goal' ? (this.map.goal_tree && this.map.goal_tree.goals || []) : this.allWork;
                const parent = source.find(candidate => number(candidate.id) === parentID);
                return parent ? { kind, item: parent, key: mapItemKey(kind, parent) } : null;
            },
            childLinks(kind, item) {
                const id = number(item && item.id);
                if (!id) return [];
                const source = kind === 'goal' ? (this.map.goal_tree && this.map.goal_tree.goals || []) : this.allWork;
                return source.filter(candidate => number(candidate.parent_id) === id).map(child => ({ kind, item: child, key: mapItemKey(kind, child) }));
            },
            navHref(route) { return routeAPI.buildRoute(route, this.routeState()); },
            issueHref(item) { return routeAPI.buildRoute('board', { ...this.routeState(), project: isProject(this.rootSlug) ? this.rootSlug : '', namespace: this.rootSlug, issueID: number(item && item.id), focus: `work:${number(item && item.id)}`, detail: false }); },
            routeState() {
                return { project: isProject(this.rootSlug) ? this.rootSlug : '', namespace: this.rootSlug, query: this.filters.query, status: this.filters.status, agent: this.filters.agent, memoryType: this.filters.memoryType, issueType: this.filters.issueType, label: this.filters.label, kinds: this.kindFilters, relations: this.relations, focus: this.selected ? this.selected.key : this.route.focus, issueID: this.selected && this.selected.kind === 'work' ? number(this.selected.item.id) : this.route.issueID, detail: this.route.detail };
            },
            syncURL(push = false) {
                const href = routeAPI.buildRoute(this.route.route, this.routeState());
                const current = window.location.pathname + window.location.search;
                if (href !== current) (push ? window.history.pushState : window.history.replaceState).call(window.history, {}, '', href);
                this.route = routeAPI.readRoute(window.location);
            },
            syncFiltersFromRoute() {
                this.filters.query = this.route.query; this.filters.status = this.route.status; this.filters.agent = this.route.agent; this.filters.memoryType = this.route.memoryType; this.filters.issueType = this.route.issueType; this.kindFilters = { ...this.route.kinds }; this.relations = { ...this.route.relations };
            },
            async navigate(route) {
                this.route = { ...this.route, route, detail: false, focus: '', issueID: 0 };
                this.selected = null;
                this.syncURL(true);
                await this.loadRoute();
            },
            handlePopState() {
                this.route = routeAPI.readRoute(window.location);
                const root = text(this.route.project || this.route.namespace);
                if (root) this.rootSlug = root;
                this.syncFiltersFromRoute();
                this.loadRoute();
            },
            async changeRoot() {
                this.rootSlug = text(this.rootSlug) || '/'; this.selected = null; this.route = { ...this.route, detail: false, focus: '', issueID: 0 }; this.syncURL(); await this.loadRoute();
            },
            resetFilters() {
                this.filters = { ...this.filters, query: '', status: '', agent: '', memoryType: '', issueType: '', label: '' }; this.kindFilters = { goal: true, work: true, memory: true, resource: true }; this.relations = { blocks: true, part_of: true, relates_to: true }; this.syncURL();
            },
            selectObject(kind, item) {
                if (!item) return;
                this.selected = { kind, item, key: mapItemKey(kind, item) };
                this.route.detail = false;
                this.syncURL();
            },
            selectMapNode(node) { this.selectObject(node.kind, node.item); },
            selectGraphNode(node) { this.selectObject('work', node.item || node); },
            selectListItem(item) { this.selectObject(this.listKind || (item.slug ? 'resource' : 'memory'), item); },
            clearSelection() { this.selected = null; this.route = { ...this.route, detail: false, focus: '', issueID: 0 }; this.syncURL(); },
            openDetail() { if (!this.selected) return; this.route.detail = true; this.syncURL(true); },
            closeDetail() { this.route.detail = false; this.syncURL(true); },
            findByFocus(focus) {
                if (!focus) return null;
                const entries = [];
                for (const goal of this.map.goal_tree && this.map.goal_tree.goals || []) entries.push({ kind: 'goal', item: goal });
                for (const work of this.allWork) entries.push({ kind: 'work', item: work });
                for (const memory of this.map.memories || []) entries.push({ kind: 'memory', item: memory });
                for (const resource of this.map.resources || []) entries.push({ kind: 'resource', item: resource });
                for (const item of this.listItems || []) entries.push({ kind: this.listKind || 'memory', item });
                return entries.find(entry => mapItemKey(entry.kind, entry.item) === focus || text(entry.item && entry.item.key) === focus || (focus === `work:${number(entry.item && entry.item.id)}` && entry.kind === 'work')) || null;
            },
            restoreFocus() {
                const focus = this.route.focus || (this.route.issueID ? `work:${this.route.issueID}` : '');
                const found = this.findByFocus(focus);
                this.selected = found ? { ...found, key: mapItemKey(found.kind, found.item) } : null;
            },
            async fetchNamespaces() {
                const result = await api.invokeTool('list_namespaces', { limit: 100, offset: 0 });
                this.namespaces = itemsOf(result).map(item => { const slug = text(item.slug || item.path); const name = text(item.name || item.title); return { ...item, slug: slug || '/', name, label: name && name !== slug ? `${name} · ${slug}` : slug || '/' }; }).filter(item => item.slug);
            },
            async bootstrap() {
                try { const headers = { Accept: 'application/json' }; if (api.token) headers.Authorization = 'Bearer ' + api.token; const response = await fetch('/auth/status', { credentials: 'same-origin', headers }); if (response.ok) this.auth = await response.json(); } catch (_) { /* 인증 상태 없이 화면을 계속 표시 */ }
                try { await this.fetchNamespaces(); } catch (error) { this.error = text(error && error.message) || '네임스페이스 목록을 불러오지 못했습니다.'; }
                const requested = text(this.route.project || this.route.namespace);
                if (requested && this.rootOptions.some(item => item.slug === requested)) this.rootSlug = requested;
                await this.loadRoute();
            },
            async loadRoute() {
                const generation = ++this.loadGeneration; this.loading = true; this.error = ''; this.listItems = [];
                try {
                    const namespace = this.rootSlug || '/';
                    this.map = emptyMap(); this.graph = { nodes: [], edges: [] }; this.plan = emptyPlan(); this.listKind = '';
                    if (this.route.route === 'goal-map' || this.route.route === 'monitor' || this.route.route === 'board') {
                        this.map = normalizeMap(unwrap(await api.invokeTool('get_goal_map', { namespace, include_done: true })));
                    } else if (this.route.route === 'graph') {
                        this.graph = normalizeGraph(unwrap(await api.invokeTool('get_work_graph', { project: isProject(namespace) ? namespace : undefined, namespaces: isProject(namespace) ? undefined : namespace, include_done: true, node_limit: 200, edge_limit: 400 })));
                    } else if (this.route.route === 'plan') {
                        this.plan = normalizePlan(unwrap(await api.invokeTool('get_work_plan', { namespace })));
                    } else if (this.route.route === 'worktrees') {
                        this.listKind = 'resource'; this.listItems = itemsOf(await api.invokeTool('list_worktrees', { namespaces: namespace, limit: 100, offset: this.route.offset }));
                    } else if (dataTools[this.route.route]) {
                        const tool = this.route.route; const args = { namespaces: namespace, q: this.filters.query, limit: 100, offset: this.route.offset }; if (this.filters.status) args.status = this.filters.status; this.listKind = dataTools[tool]; this.listItems = itemsOf(await api.invokeTool(tool, args));
                    } else if (this.route.route === 'list_namespaces') {
                        this.listKind = 'resource'; this.listItems = this.namespaces;
                    }
                    if (generation === this.loadGeneration) this.restoreFocus();
                } catch (error) {
                    if (generation === this.loadGeneration) this.error = text(error && error.message) || `${this.pageTitle}을(를) 불러오지 못했습니다.`;
                } finally { if (generation === this.loadGeneration) this.loading = false; }
            },
            listItemKey(item) { return item && (item.key || item.slug || `${this.listKind}:${item.id || item.memory_id || item.resource_id}`); },
            listItemTitle(item) {
                if (this.route.route === 'list_namespaces') return item.name && item.name !== item.slug ? item.name : item.slug === '/' ? '기본 공간' : item.slug;
                return this.listKind === 'goal' ? itemTitle('goal', item) : this.listKind === 'resource' && item.slug ? (item.name || item.slug) : itemTitle(this.listKind, item);
            },
            listItemSummary(item) {
                if (this.route.route === 'list_namespaces') return item.name && item.name !== item.slug ? `${item.slug}${item.description ? ' · ' + item.description : ''}` : item.description || '';
                return item.summary || item.description || item.verification_plan || item.value || item.property || item.uri || (this.listKind === 'resource' ? '' : item.slug) || '';
            },
            changeTheme(value) { try { localStorage.setItem('stash.theme', ['system', 'light', 'dark'].includes(value) ? value : 'system'); } catch (_) {} if (root.stashConsoleApplyTheme) root.stashConsoleApplyTheme(); },
            markAuthenticationExpired() {
                api.token = '';
                api.sessionId = '';
                try { sessionStorage.removeItem('stash.apiToken'); } catch (_) {}
                this.auth = { ...(this.auth || {}), authenticated: false, user: '' };
                this.authPanelOpen = false;
                this.issuedToken = '';
                this.tokenError = '로그인이 만료되었습니다. 다시 로그인하세요.';
            },
            logout() {
                api.token = '';
                api.sessionId = '';
                try { sessionStorage.removeItem('stash.apiToken'); } catch (_) {}
                this.authPanelOpen = false;
                this.issuedToken = '';
                window.location.assign('/auth/logout');
            },
            async issueToken() {
                if (this.tokenLoading) return; this.tokenLoading = true; this.tokenError = '';
                try { const response = await fetch('/auth/token', { method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' } }); const body = await response.json().catch(() => ({})); if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`); this.issuedToken = text(body.token); this.authPanelOpen = true; } catch (error) { this.tokenError = text(error && error.message) || '토큰을 발급하지 못했습니다.'; } finally { this.tokenLoading = false; }
            },
            async copyIssuedToken() { if (!this.issuedToken || !navigator.clipboard) return; try { await navigator.clipboard.writeText(this.issuedToken); } catch (_) {} }
        }
    });
    app.mount(document.querySelector('[data-stash-vue-console]'));
}(typeof globalThis !== 'undefined' ? globalThis : window));
