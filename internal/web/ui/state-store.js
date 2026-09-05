(function (root, factory) {
    const api = factory(typeof module === 'object' && module.exports ? require('./console-i18n.js') : root.StashI18n);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashStateStore = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function (i18n) {
    'use strict';

    function createStateStore() {
        return {
            token: '',
            adminToken: '',
            isAdmin: false,
            adminConfigured: false,
            adminChecked: false,
            adminLoading: false,
            maintenanceAction: false,
            adminError: '',
            maintenance: { model: '', dimensions: 0, episodes_total: 0, facts_total: 0, episodes_pending: 0, facts_pending: 0, pending: 0, due: 0, failed: 0, paused: 0, latest_error: '' },
            sessionId: '',
            requestId: 0,
            auth: { auth_mode: 'none', authenticated: false, user: '' },
            authChecked: false,
            authLoading: true,
            authError: '',
            issuedToken: '',
            issuedTokenExpiresIn: 0,
            issuedTokenIssuedAt: 0,
            tokenIssueLoading: false,
            tokenIssueError: '',
            tokenCopyStatus: '복사',
            result: 'Stash 콘솔입니다.\n\n왼쪽 메뉴에서 작업이나 기억을 선택하세요.',
            resultValue: null,
            resultKind: '',
            resultTitle: '결과',
            resultDescription: '선택한 항목의 응답을 표시합니다.',
            loading: false,
            notice: { text: '', type: 'success' },
            lastLoadedAt: null,
            noticeTimer: null,
            view: 'goal-map',
            activeNav: 'goal-map',
            tokenOpen: false,
            listQuery: '',
            listStatus: '',
            pageSize: 50,
            listPage: { tool: '', args: {}, offset: 0, nextOffset: 0, limit: 50, hasNext: false, history: [] },
            listError: '',
            boardPage: { offset: 0, nextOffset: 0, limit: 50, hasNext: false, history: [] },
            worktreePage: { offset: 0, nextOffset: 0, limit: 50, hasNext: false, history: [] },
            commentPage: { offset: 0, nextOffset: 0, limit: 50, hasNext: false, history: [] },
            draggedItem: null,
            boardError: '',
            boardFilter: { q: '', issueType: '', label: '' },
            issueFormOpen: false,
            issueForm: { title: '', description: '', issueType: 'task', labels: '' },
            selectedIssue: null,
            selectedComments: [],
            selectedMemoryLinks: [],
            commentBody: '',
            copyStatus: '복사',
            agentGuide: i18n.translate('en', 'agent.guide'),
            boardColumns: [
                { status: 'backlog', label: '대기' },
                { status: 'ready', label: '준비' },
                { status: 'doing', label: '진행 중' },
                { status: 'blocked', label: '막힘' },
                { status: 'review', label: '검토' },
                { status: 'done', label: '완료' },
                { status: 'canceled', label: '취소' }
            ],
        };
    }

    return { createStateStore };
}));
