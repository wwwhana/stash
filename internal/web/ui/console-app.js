(function (root, factory) {
    const commonJS = typeof module === 'object' && module.exports;
    const api = commonJS
        ? factory(
            require('./state-store.js'),
            require('./api-client.js'),
            require('./route-view-model.js'),
            require('./work-plan-view-model.js'),
            require('./issue-execution-view-model.js'),
            require('./map-scope-view-model.js'),
            require('./work-graph-view-model.js'),
            require('./goal-map-view-model.js')
        )
        : factory(
            root.StashStateStore,
            root.StashApiClient,
            root.StashRouteViewModel,
            root.StashWorkPlanViewModel,
            root.StashIssueExecutionViewModel,
            root.StashMapScopeViewModel,
            root.StashWorkGraphViewModel,
            root.StashGoalMapViewModel
        );
    if (commonJS) {
        module.exports = api;
    } else {
        root.StashConsoleApp = api;
        root.stashConsole = api.createConsoleViewModel;
    }
}(typeof globalThis !== 'undefined' ? globalThis : this, function (
    stateStore,
    apiClient,
    routeViewModel,
    workPlanViewModel,
    issueExecutionViewModel,
    mapScopeViewModel,
    workGraphViewModel,
    goalMapViewModel
) {
    'use strict';

    function createConsoleViewModel() {
        return {
            ...routeViewModel.createRouteViewModel(),
            ...workPlanViewModel.createWorkPlanViewModel(),
            ...issueExecutionViewModel.createIssueExecutionViewModel(),
            ...mapScopeViewModel.createMapScopeViewModel(),
            ...workGraphViewModel.createWorkGraphViewModel(),
            ...goalMapViewModel.createGoalMapViewModel(),
            ...stateStore.createStateStore(),
            ...apiClient.createApiClient(),
            init() {
                window.addEventListener('popstate', () => {
                    if (!this.authChecked || (this.authRequired() && !this.auth.authenticated)) return;
                    this.restoreRoute();
                });
                this.bootstrap();
            },

            async bootstrap() {
                this.authChecked = false;
                this.authLoading = true;
                this.authError = '';
                this.sessionId = '';
                await this.loadAuthStatus();
                this.loadAdminStatus();
                if (!this.authRequired() || this.auth.authenticated) {
                    if (!this.planActor) this.planActor = this.auth.user || 'codex';
                    await this.ensureWorkspace();
                    await this.restoreRoute();
                }
            },

            async ensureWorkspace() {
                await this.invokeTool('create_namespace', {
                    slug: '/', name: 'Workspace', description: 'Default workspace for this Stash user.'
                });
            },

            normalizeToken() {
                this.token = this.token.trim();
            },

            normalizeAdminToken() {
                this.adminToken = this.adminToken.trim();
            },

            setNotice(text, type = 'success', timeout = 3200) {
                if (this.noticeTimer) window.clearTimeout(this.noticeTimer);
                this.notice = { text, type };
                if (timeout > 0) {
                    this.noticeTimer = window.setTimeout(() => {
                        this.notice = { text: '', type: 'success' };
                        this.noticeTimer = null;
                    }, timeout);
                }
            },

            markLoaded() {
                this.lastLoadedAt = new Date();
            },

            formatTime(value) {
                const date = value instanceof Date ? value : new Date(value);
                if (Number.isNaN(date.getTime())) return '';
                return '업데이트 ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            },

            refreshCurrentView() {
                if (this.loading) return;
                if (this.view === 'board') return this.loadWorkBoard(false);
                if (this.view === 'goal-map') return this.loadGoalMap(false);
                if (this.view === 'plan') return this.loadWorkPlan();
                if (this.view === 'graph') return this.loadWorkGraph();
                if (this.view === 'worktrees') return this.loadWorktrees(false);
                if (this.view === 'maintenance') return this.loadMaintenance();
                if (this.view === 'agent') return this.showAgentGuide();
                if (this.listPage.tool) return this.loadListPage();
            },

            applyMaintenanceStatus(value) {
                const status = value && typeof value === 'object' ? value : {};
                this.maintenance = {
                    ...this.maintenance,
                    ...status,
                    latest_error: status.latest_error || ''
                };
            },

            async loadAdminStatus(showError = false) {
                this.adminChecked = false;
                this.adminLoading = true;
                this.adminError = '';
                try {
                    const body = await this.adminRequest('/admin/maintenance/embeddings');
                    this.applyMaintenanceStatus(body);
                    this.isAdmin = true;
                } catch (e) {
                    this.isAdmin = false;
                    if (e.status >= 500) this.adminError = e.message;
                    if (showError) {
                        this.setNotice(this.adminError || '관리자 권한을 확인하지 못했습니다.', 'error', 0);
                    }
                } finally {
                    this.adminChecked = true;
                    this.adminLoading = false;
                    this.syncRoute();
                }
            },

            showMaintenance() {
                if (!this.isAdmin) return;
                this.activeNav = 'maintenance';
                this.resultTitle = '임베딩 관리';
                this.resultDescription = '대기 중인 벡터 작업과 최근 오류를 확인합니다.';
                this.view = 'maintenance';
                this.loadMaintenance();
            },

            async loadMaintenance() {
                if (!this.isAdmin && this.adminChecked) return;
                this.view = 'maintenance';
                this.activeNav = 'maintenance';
                this.resultTitle = '임베딩 관리';
                this.resultDescription = '대기 중인 벡터 작업과 최근 오류를 확인합니다.';
                this.adminLoading = true;
                try {
                    const body = await this.adminRequest('/admin/maintenance/embeddings');
                    this.applyMaintenanceStatus(body);
                    this.isAdmin = true;
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (e) {
                    this.isAdmin = false;
                    this.adminError = e.message;
                    this.setNotice('임베딩 상태를 불러오지 못했습니다: ' + e.message, 'error', 0);
                } finally {
                    this.adminChecked = true;
                    this.adminLoading = false;
                    this.syncRoute();
                }
            },

            async forceRetryEmbeddings() {
                if (this.maintenanceAction || !this.isAdmin) return;
                if (!window.confirm('예약된 임베딩 실패 항목을 지금 다시 시도할까요?')) return;
                this.maintenanceAction = true;
                try {
                    const body = await this.adminRequest('/admin/maintenance/embeddings/retry', { method: 'POST' });
                    this.applyMaintenanceStatus(body.status);
                    this.markLoaded();
                    this.setNotice(`${body.woken || 0}개 항목을 다시 시도하도록 깨웠습니다.`, 'success');
                } catch (e) {
                    this.setNotice('재시도를 시작하지 못했습니다: ' + e.message, 'error', 0);
                } finally {
                    this.maintenanceAction = false;
                }
            },

            async forceReindexEmbeddings() {
                if (this.maintenanceAction || !this.isAdmin) return;
                if (!window.confirm('모든 벡터를 비우고 원문을 다시 계산할까요?')) return;
                this.maintenanceAction = true;
                try {
                    const body = await this.adminRequest('/admin/maintenance/embeddings/reindex', { method: 'POST' });
                    this.applyMaintenanceStatus(body.status);
                    this.markLoaded();
                    this.setNotice(`${body.queued || 0}개 원문을 다시 계산하도록 등록했습니다.`, 'success');
                } catch (e) {
                    this.setNotice('전체 다시 계산을 시작하지 못했습니다: ' + e.message, 'error', 0);
                } finally {
                    this.maintenanceAction = false;
                }
            },

            userInitials() {
                const value = String((this.auth && this.auth.user) || '사용자').trim();
                const words = value.split(/[\s@._-]+/).filter(Boolean);
                if (words.length > 1) return (words[0][0] + words[1][0]).toUpperCase();
                return value.slice(0, 2).toUpperCase();
            },

            async loadAuthStatus() {
                try {
                    const res = await fetch('/auth/status');
                    if (!res.ok) {
                        throw new Error(`HTTP ${res.status}`);
                    }
                    const status = await res.json();
                    if (!status || typeof status !== 'object') {
                        throw new Error('인증 상태 응답이 올바르지 않습니다.');
                    }
                    this.auth = {
                        ...status,
                        auth_mode: status.auth_mode || status.mode || 'oidc',
                        authenticated: status.authenticated === true,
                        user: status.user || ''
                    };
                } catch (_) {
                    // Fail closed: do not render the workspace when auth status is unknown.
                    this.auth = { auth_mode: 'oidc', authenticated: false, user: '' };
                    this.authError = '인증 상태를 확인할 수 없습니다.';
                } finally {
                    this.authChecked = true;
                    this.authLoading = false;
                }
            },

            async logout() {
                this.executionLeaseTokens = {};
                this.executionPendingMutation = null;
                await fetch('/auth/logout', { method: 'POST' });
                this.sessionId = '';
                window.location.reload();
            },

            async callTool(toolName, args, offset = 0) {
                this.view = 'text';
                this.activeNav = toolName;
                this.resultKind = toolName;
                this.resultTitle = this.toolLabel(toolName);
                this.resultDescription = '선택한 기억 항목의 응답입니다.';
                this.listPage = { tool: toolName, args: { ...args }, offset: Math.max(0, Number(offset) || 0), nextOffset: 0, limit: this.pageSize, hasNext: false, history: [] };
                await this.loadListPage();
            },

            async loadListPage() {
                this.loading = true;
                this.result = '불러오는 중...';
                try {
                    const args = {
                        ...this.listPage.args,
                        limit: this.listPage.limit + 1,
                        offset: this.listPage.offset
                    };
                    const data = await this.invokeTool(this.listPage.tool, args);
                    const value = this.toolValue(data);
                    const pageLimit = this.listPage.limit;
                    const page = this.pageSlice(value, pageLimit, this.listPage.offset);
                    this.listPage.hasNext = page.hasMore;
                    this.listPage.nextOffset = page.nextOffset;
                    this.resultValue = page.isPage ? page.items : value;
                    this.result = JSON.stringify(this.resultValue, null, 2);
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (e) {
                    this.resultValue = null;
                    this.result = '오류: ' + e.message;
                    this.setNotice('불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            async previousListPage() {
                if (this.listPage.offset <= 0 || this.loading) return;
                this.listPage.offset = this.listPage.history.length
                    ? this.listPage.history.pop()
                    : Math.max(0, this.listPage.offset - this.listPage.limit);
                await this.loadListPage();
            },

            async nextListPage() {
                if (!this.listPage.hasNext || this.loading) return;
                this.listPage.history.push(this.listPage.offset);
                this.listPage.offset = this.listPage.nextOffset;
                await this.loadListPage();
            },

            authMode() {
                return (this.auth && (this.auth.auth_mode || this.auth.mode)) || 'oidc';
            },

            authRequired() {
                return ['oauth', 'oidc'].includes(this.authMode());
            },

            resultItems() {
                if (Array.isArray(this.resultValue)) return this.resultValue;
                if (this.resultValue && typeof this.resultValue === 'object') return [this.resultValue];
                return [];
            },

            resultItemKind() {
                return {
                    list_namespaces: '네임스페이스',
                    query_facts: '사실',
                    list_hypotheses: '가설',
                    list_goals: '목표',
                    list_worktrees: 'Git 작업 공간'
                }[this.resultKind] || '항목';
            },

            resultItemTitle(item) {
                if (!item || typeof item !== 'object') return String(item ?? '');
                return item.name || item.content || item.title || item.slug || item.description || '항목';
            },

            resultItemBody(item) {
                if (!item || typeof item !== 'object') return '';
                const title = this.resultItemTitle(item);
                if (item.description && item.description !== title) return item.description;
                if (item.verification_plan) return item.verification_plan;
                if (item.notes) return item.notes;
                if (item.value && item.value !== title) return item.value;
                return '';
            },

            resultItemMeta(item) {
                if (!item || typeof item !== 'object') return '';
                if (item.slug) return item.slug;
                if (item.entity) return item.entity;
                if (item.author) return item.author;
                if (item.created_at) return new Date(item.created_at).toLocaleDateString();
                return '';
            },

            hasConfidence(item) {
                return item && Number.isFinite(Number(item.confidence));
            },

            confidenceLabel(item) {
                return '확신도 ' + Math.round(Number(item.confidence) * 100) + '%';
            },

            hasPriority(item) {
                return item && item.priority !== undefined && item.priority !== null && String(item.priority).trim() !== '';
            },

            priorityLabel(item) {
                return '우선순위 ' + String(item.priority);
            },

            itemLabels(item) {
                if (!item || item.labels === undefined || item.labels === null) return [];
                if (Array.isArray(item.labels)) return item.labels;
                return String(item.labels).split(',').map(label => label.trim()).filter(Boolean);
            },

            pageLabel(page) {
                const depth = page && Array.isArray(page.history) ? page.history.length : 0;
                return `${depth + 1}쪽`;
            },

            boardHasFilters() {
                return Boolean(String(this.boardFilter.q || '').trim() || this.boardFilter.issueType || String(this.boardFilter.label || '').trim());
            },

            async clearBoardFilters() {
                this.boardFilter = { q: '', issueType: '', label: '' };
                await this.loadWorkBoard();
            },

            async loadWorkBoard(resetPage = true) {
                this.activeNav = 'board';
                this.resultTitle = '이슈 보드';
                this.resultDescription = '작업을 상태별로 정리합니다.';
                if (resetPage) {
                    this.boardPage.offset = 0;
                    this.boardPage.history = [];
                }
                await this.loadWorkView('board');
            },

            async loadWorktrees(resetPage = true) {
                this.activeNav = 'worktrees';
                this.resultTitle = 'Git 작업 공간';
                this.resultDescription = '연결된 Git 작업 공간의 현재 상태입니다.';
                this.view = 'worktrees';
                if (resetPage) {
                    this.worktreePage.offset = 0;
                    this.worktreePage.history = [];
                }
                this.loading = true;
                this.result = '불러오는 중...';
                try {
                    const data = await this.invokeTool('list_worktrees', {
                        namespaces: '/',
                        limit: this.worktreePage.limit + 1,
                        offset: this.worktreePage.offset
                    });
                    const value = this.toolValue(data);
                    const page = this.pageSlice(value, this.worktreePage.limit, this.worktreePage.offset);
                    this.worktreePage.hasNext = page.hasMore;
                    this.worktreePage.nextOffset = page.nextOffset;
                    this.setWorkGraphWorktrees(page.items);
                    this.resultValue = this.graph.worktrees;
                    this.resultKind = 'list_worktrees';
                    this.result = JSON.stringify(this.graph.worktrees, null, 2);
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (e) {
                    this.resultValue = null;
                    this.result = '오류: ' + e.message;
                    this.view = 'text';
                    this.setNotice('Git 작업 공간을 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            async previousWorktreePage() {
                if (this.worktreePage.offset <= 0 || this.loading) return;
                this.worktreePage.offset = this.worktreePage.history.length
                    ? this.worktreePage.history.pop()
                    : Math.max(0, this.worktreePage.offset - this.worktreePage.limit);
                await this.loadWorktrees(false);
            },

            async nextWorktreePage() {
                if (!this.worktreePage.hasNext || this.loading) return;
                this.worktreePage.history.push(this.worktreePage.offset);
                this.worktreePage.offset = this.worktreePage.nextOffset;
                await this.loadWorktrees(false);
            },

            showAgentGuide() {
                this.activeNav = 'agent';
                this.resultTitle = '에이전트 규칙';
                this.resultDescription = '프로젝트에 복사해 사용할 작업 규칙입니다.';
                this.view = 'agent';
                this.copyStatus = '복사';
                this.markLoaded();
                this.syncRoute();
            },

            async copyAgentGuide() {
                try {
                    if (navigator.clipboard && window.isSecureContext) {
                        await navigator.clipboard.writeText(this.agentGuide);
                    } else {
                        const input = document.createElement('textarea');
                        input.value = this.agentGuide;
                        input.style.position = 'fixed';
                        input.style.opacity = '0';
                        document.body.appendChild(input);
                        input.select();
                        const copied = document.execCommand('copy');
                        input.remove();
                        if (!copied) throw new Error('copy command was rejected');
                    }
                    this.copyStatus = '복사됨';
                    window.setTimeout(() => { this.copyStatus = '복사'; }, 1600);
                } catch (_) {
                    this.copyStatus = '복사 실패';
                }
            },

            async loadWorkView(view) {
                this.view = view;
                if (view === 'board') {
                    this.resultTitle = '이슈 보드';
                    this.resultDescription = '작업을 상태별로 정리합니다.';
                    this.boardError = '';
                } else if (view === 'graph') {
                    this.resultTitle = '작업 흐름';
                    this.resultDescription = this.graphScopeLabel();
                    this.graphError = '';
                }
                this.clearWorkGraph();
                this.loading = true;
                this.result = '불러오는 중...';
                try {
                    let graph;
                    if (view === 'board') {
                        const data = await this.invokeTool('list_work_items', {
                            namespaces: '/', q: this.boardFilter.q, issue_type: this.boardFilter.issueType,
                            label: this.boardFilter.label,
                            limit: this.boardPage.limit + 1,
                            offset: this.boardPage.offset
                        });
                        const value = this.toolValue(data);
                        const page = this.pageSlice(value, this.boardPage.limit, this.boardPage.offset);
                        this.boardPage.hasNext = page.hasMore;
                        this.boardPage.nextOffset = page.nextOffset;
                        graph = { nodes: page.items, edges: [], worktrees: [] };
                    } else {
                        const graphArgs = { include_done: true };
                        if (this.mapNamespaceSlug) graphArgs.project = this.mapNamespaceSlug;
                        else graphArgs.namespaces = '/';
                        const data = await this.invokeTool('get_work_graph', graphArgs);
                        graph = this.toolValue(data);
                    }
                    this.setWorkGraph(graph);
                    this.resultValue = this.graph;
                    this.resultKind = view === 'board' ? 'list_work_items' : 'get_work_graph';
                    this.result = JSON.stringify(this.graph, null, 2);
                    this.markLoaded();
                    this.setNotice('', 'success', 0);
                } catch (e) {
                    this.resultValue = null;
                    this.result = '오류: ' + e.message;
                    this.clearWorkGraph();
                    if (view === 'board') {
                        this.boardPage.hasNext = false;
                        this.boardError = '잠시 후 다시 시도하세요.';
                    } else {
                        this.graphError = '그래프를 불러오지 못했습니다.';
                    }
                    this.setNotice(view === 'board' ? '이슈를 불러오지 못했습니다.' : '그래프를 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            async previousBoardPage() {
                if (this.boardPage.offset <= 0 || this.loading) return;
                this.boardPage.offset = this.boardPage.history.length
                    ? this.boardPage.history.pop()
                    : Math.max(0, this.boardPage.offset - this.boardPage.limit);
                await this.loadWorkView('board');
            },

            async nextBoardPage() {
                if (!this.boardPage.hasNext || this.loading) return;
                this.boardPage.history.push(this.boardPage.offset);
                this.boardPage.offset = this.boardPage.nextOffset;
                await this.loadWorkView('board');
            },

            boardItems(status) {
                return this.graph.nodes
                    .filter(item => item.status === status)
                    .slice()
                    .sort((a, b) => (Number(a.position) || 0) - (Number(b.position) || 0));
            },

            startWorkItemDrag(item, event) {
                this.draggedItem = null;
                if (this.hasActiveIssueAttempt(item)) {
                    if (event) event.preventDefault();
                    this.setNotice('실행 중에는 카드를 옮길 수 없습니다.', 'error', 0);
                    return;
                }
                this.draggedItem = item && item.id;
            },

            async dropWorkItem(status) {
                const id = this.draggedItem;
                this.draggedItem = null;
                if (!id) return;
                const item = this.graph.nodes.find(candidate => candidate.id === id);
                if (!item || item.status === status) return;
                if (this.hasActiveIssueAttempt(item)) {
                    this.setNotice('실행 중에는 카드를 옮길 수 없습니다.', 'error', 0);
                    return;
                }
                if (this.issueCompletionRequiresFinishWork(status, item)) {
                    this.setNotice('완료 조건이 있는 작업은 ‘조건 확인 후 완료’에서 끝내세요.', 'error', 0);
                    return;
                }
                this.loading = true;
                try {
                    const destination = this.boardItems(status).filter(candidate => candidate.id !== id);
                    const position = destination.reduce((max, candidate) => Math.max(max, Number(candidate.position) || 0), -1) + 1;
                    await this.invokeTool('update_work_item', { id, status, position });
                    await this.loadWorkView('board');
                    this.setNotice('상태를 저장했습니다.');
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    this.setNotice('상태를 저장하지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            async createIssue() {
                this.loading = true;
                try {
                    await this.invokeTool('create_work_item', {
                        namespace: '/', title: this.issueForm.title, description: this.issueForm.description,
                        issue_type: this.issueForm.issueType, labels: this.issueForm.labels, status: 'backlog'
                    });
                    this.issueForm = { title: '', description: '', issueType: 'task', labels: '' };
                    this.issueFormOpen = false;
                    await this.loadWorkBoard();
                    this.setNotice('이슈를 만들었습니다.');
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    this.setNotice('이슈를 만들지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            async changeIssueStatus(status, control) {
                if (!this.selectedIssue || this.loading) return;
                const previous = this.selectedIssue.status;
                if (!status || status === previous) return;
                const restore = () => {
                    if (control) control.value = previous;
                };
                if (this.executionLoading || this.executionError || this.hasActiveIssueAttempt()) {
                    restore();
                    this.setNotice(this.issueStatusGuardMessage() || '실행 상태를 확인한 뒤 바꿀 수 있습니다.', 'error', 0);
                    return;
                }
                if (this.issueCompletionRequiresFinishWork(status)) {
                    restore();
                    this.setNotice('완료 조건이 있는 작업은 ‘조건 확인 후 완료’에서 끝내세요.', 'error', 0);
                    return;
                }
                const id = this.selectedIssue.id;
                const destination = this.boardItems(status).filter(candidate => Number(candidate.id) !== Number(id));
                const position = destination.reduce((max, candidate) => Math.max(max, Number(candidate.position) || 0), -1) + 1;
                this.loading = true;
                try {
                    await this.invokeTool('update_work_item', { id, status, position });
                    this.selectedIssue = { ...this.selectedIssue, status, position };
                    const graphItem = this.graph.nodes.find(candidate => Number(candidate.id) === Number(id));
                    if (graphItem) this.replaceWorkGraphNode({ ...graphItem, status, position });
                    this.setNotice('상태를 저장했습니다.');
                } catch (e) {
                    restore();
                    this.result = '오류: ' + e.message;
                    this.setNotice('상태를 저장하지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            async openIssue(id, trigger = null) {
                this.loading = true;
                this.resetIssueExecution();
                this.executionLoading = true;
                this.selectedComments = [];
                this.selectedMemoryLinks = [];
                this.selectedIssue = this.graph.nodes.find(item => Number(item.id) === Number(id)) || { id, title: '이슈' };
                this.openIssueDrawer(trigger);
                try {
                    const item = this.toolValue(await this.invokeTool('get_work_item', { id }));
                    this.selectedIssue = item;
                    this.commentPage = { offset: 0, nextOffset: 0, limit: this.pageSize, hasNext: false, history: [] };
                    const [links] = await Promise.all([
                        this.invokeTool('list_work_item_memory_links', { work_item_id: id }).then(data => this.toolValue(data)),
                        this.loadIssueExecution(id),
                        this.loadIssueComments(id)
                    ]);
                    this.selectedMemoryLinks = Array.isArray(links) ? links : [];
                    this.markLoaded();
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    if (!this.executionLoaded) {
                        this.executionLoading = false;
                        this.executionError = this.executionFailureMessage(e, '실행 기록을 불러오지 못했습니다.');
                    }
                    this.setNotice('이슈를 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            closeIssue() {
                if (this.currentExecutionLeaseToken() && this.executionStatusValue() === 'active') {
                    this.setNotice('작업을 인계하거나 완료한 뒤 닫으세요.', 'error', 0);
                    return;
                }
                this.selectedIssue = null;
                this.selectedComments = [];
                this.selectedMemoryLinks = [];
                this.commentBody = '';
                this.commentPage = { offset: 0, nextOffset: 0, limit: this.pageSize, hasNext: false, history: [] };
                this.resetIssueExecution(true);
                this.closeIssueDrawer();
                this.syncRoute(true);
            },

            async loadIssueComments(workItemID = this.selectedIssue && this.selectedIssue.id) {
                if (!workItemID) return;
                const data = await this.invokeTool('list_work_item_comments', {
                    work_item_id: workItemID,
                    limit: this.commentPage.limit + 1,
                    offset: this.commentPage.offset
                });
                const value = this.toolValue(data);
                const page = this.pageSlice(value, this.commentPage.limit, this.commentPage.offset);
                this.commentPage.hasNext = page.hasMore;
                this.commentPage.nextOffset = page.nextOffset;
                this.selectedComments = page.items;
            },

            async previousCommentPage() {
                if (!this.selectedIssue || this.commentPage.offset <= 0 || this.loading) return;
                this.commentPage.offset = this.commentPage.history.length
                    ? this.commentPage.history.pop()
                    : Math.max(0, this.commentPage.offset - this.commentPage.limit);
                this.loading = true;
                try {
                    await this.loadIssueComments();
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    this.setNotice('댓글을 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            async nextCommentPage() {
                if (!this.selectedIssue || !this.commentPage.hasNext || this.loading) return;
                this.commentPage.history.push(this.commentPage.offset);
                this.commentPage.offset = this.commentPage.nextOffset;
                this.loading = true;
                try {
                    await this.loadIssueComments();
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    this.setNotice('댓글을 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            async addComment() {
                if (!this.selectedIssue || !this.commentBody.trim()) return;
                this.loading = true;
                try {
                    await this.invokeTool('add_work_item_comment', { work_item_id: this.selectedIssue.id, body: this.commentBody });
                    this.commentBody = '';
                    this.commentPage.offset = 0;
                    this.commentPage.history = [];
                    await this.loadIssueComments();
                    this.setNotice('댓글을 남겼습니다.');
                } catch (e) {
                    this.result = '오류: ' + e.message;
                    this.setNotice('댓글을 남기지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            issueTypeLabel(issueType) {
                return { task: '작업', bug: '버그', feature: '기능', chore: '정리', question: '질문' }[issueType] || issueType || '작업';
            },

            statusLabel(status) {
                return {
                    backlog: '대기', ready: '준비', doing: '진행 중', blocked: '막힘',
                    review: '검토', done: '완료', canceled: '취소', active: '진행 중',
                    completed: '완료', abandoned: '중단'
                }[status] || status || '대기';
            },

            worktreeStatusLabel(status) {
                return {
                    clean: '깨끗함', dirty: '변경 있음', missing: '찾을 수 없음',
                    merged: '병합됨', removed: '제거됨'
                }[status] || status || '확인 필요';
            },

            toolLabel(toolName) {
                return {
                    list_namespaces: '네임스페이스',
                    query_facts: '사실',
                    list_hypotheses: '가설',
                    list_goals: '목표'
                }[toolName] || '조회 결과';
            },

            viewTitle() {
                return this.resultTitle || '결과';
            },

            worktreeStatusClass(status) {
                return {
                    clean: 'bg-emerald-50 text-emerald-700', dirty: 'bg-amber-50 text-amber-700',
                    missing: 'bg-red-50 text-red-700', merged: 'bg-indigo-50 text-indigo-700',
                    removed: 'bg-gray-200 text-gray-600'
                }[status] || 'bg-gray-100 text-gray-600';
            },

        };
    }

    return { createConsoleViewModel };
}));
