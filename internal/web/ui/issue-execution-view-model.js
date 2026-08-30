(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashIssueExecutionViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createIssueExecutionViewModel() {
        const emptyExecution = () => ({
            attempt: null,
            conditions: [],
            evidence: [],
            worktreeLinks: [],
            checkpoint: null,
            blockers: [],
            nextAction: ''
        });
        const emptyPrepareCondition = () => ({ kind: 'custom', description: '', verification: '' });
        const emptyForm = () => ({
            agent: '',
            worktreeId: '',
            leaseMinutes: 30,
            nextAction: '',
            summary: '',
            result: '',
            prepareConditions: [emptyPrepareCondition()],
            conditionId: '',
            evidenceKind: 'test',
            evidenceSummary: '',
            evidenceReference: ''
        });
        let issueTrigger = null;

        return {
            workExecution: emptyExecution(),
            selectedIssueExecution: null,
            executionForm: emptyForm(),
            executionFormMode: '',
            executionLoading: false,
            executionLoaded: false,
            executionError: '',
            executionAction: '',
            executionLeaseTokens: {},
            executionPendingMutation: null,
            executionEvidenceDrafts: {},

            resetIssueExecution(clearSecrets = false) {
                this.workExecution = emptyExecution();
                this.selectedIssueExecution = null;
                this.executionForm = emptyForm();
                this.executionFormMode = '';
                this.executionLoading = false;
                this.executionLoaded = false;
                this.executionError = '';
                this.executionAction = '';
                this.executionEvidenceDrafts = {};
                if (clearSecrets) {
                    this.executionLeaseTokens = {};
                    this.executionPendingMutation = null;
                }
            },

            openIssueDrawer(trigger) {
                issueTrigger = trigger instanceof HTMLElement ? trigger : document.activeElement;
                document.body.classList.add('stash-drawer-open');
                this.$nextTick(() => {
                    window.requestAnimationFrame(() => {
                        if (this.$refs.issueDrawerClose) this.$refs.issueDrawerClose.focus();
                    });
                });
            },

            closeIssueDrawer() {
                document.body.classList.remove('stash-drawer-open');
                const trigger = issueTrigger;
                issueTrigger = null;
                this.$nextTick(() => {
                    if (trigger && trigger.isConnected) trigger.focus();
                });
            },

            trapIssueDrawerFocus(event) {
                const drawer = this.$refs.issueDrawer;
                if (!drawer) return;
                const focusable = Array.from(drawer.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'))
                    .filter(element => !element.hidden && element.getClientRects().length > 0);
                if (!focusable.length) {
                    event.preventDefault();
                    return;
                }
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                if (event.shiftKey && document.activeElement === first) {
                    event.preventDefault();
                    last.focus();
                } else if (!event.shiftKey && document.activeElement === last) {
                    event.preventDefault();
                    first.focus();
                }
            },

            executionArray(...values) {
                for (const value of values) {
                    if (Array.isArray(value)) return value;
                }
                return [];
            },

            applyIssueExecution(value) {
                const response = value && typeof value === 'object' ? value : {};
                this.selectedIssueExecution = response;
                const root = response.execution && typeof response.execution === 'object'
                    ? response.execution
                    : (response.resume && typeof response.resume === 'object' ? response.resume : response);
                const workItem = root.work_item || response.work_item;
                const attempts = this.executionArray(root.attempts, response.attempts);
                const attempt = root.active_attempt || root.current_attempt || root.latest_attempt || root.attempt || response.active_attempt || response.latest_attempt ||
                    attempts.find(candidate => candidate && !this.executionStatusClosed(candidate.status)) || attempts[0] || null;
                const checkpoints = this.executionArray(root.checkpoints, response.checkpoints, attempt && attempt.checkpoints);
                const checkpoint = root.last_checkpoint || root.latest_checkpoint || response.last_checkpoint ||
                    (checkpoints.length ? checkpoints[0] : null);
                const conditions = this.executionArray(
                    root.conditions, root.required_conditions, root.completion_conditions,
                    response.conditions, response.required_conditions,
                    attempt && attempt.conditions, attempt && attempt.required_conditions
                ).slice().sort((a, b) => (Number(a && a.position) || 0) - (Number(b && b.position) || 0));
                const issue = workItem && typeof workItem === 'object' ? workItem : this.selectedIssue;
                if (issue && typeof issue === 'object') {
                    const executionIssue = { ...issue, latest_attempt: attempt || null, completion_conditions: conditions };
                    this.selectedIssue = executionIssue;
                    if (this.graph && Array.isArray(this.graph.nodes)) this.replaceWorkGraphNode(executionIssue);
                }
                const evidence = this.executionArray(
                    root.recent_evidence, root.evidence, response.recent_evidence, response.evidence,
                    attempt && attempt.recent_evidence, attempt && attempt.evidence
                ).slice().sort((a, b) => {
                    const left = new Date(a && (a.created_at || a.submitted_at) || 0).getTime();
                    const right = new Date(b && (b.created_at || b.submitted_at) || 0).getTime();
                    return right - left;
                });
                const worktreeLinks = this.executionArray(root.worktree_links, response.worktree_links);
                const blockers = this.executionArray(
                    root.blockers, response.blockers, checkpoint && checkpoint.blockers, attempt && attempt.blockers
                );
                this.workExecution = {
                    attempt,
                    conditions,
                    evidence,
                    worktreeLinks,
                    checkpoint,
                    blockers,
                    nextAction: String(root.next_action || response.next_action || (attempt && attempt.next_action) || (checkpoint && checkpoint.next_action) || '').trim()
                };
                const linkedEvidence = new Set(conditions.flatMap(condition => Array.isArray(condition.evidence_ids) ? condition.evidence_ids.map(Number) : []));
                const drafts = {};
                for (const item of evidence) {
                    const evidenceID = Number(item && item.id) || 0;
                    let payload = item && item.payload;
                    if (typeof payload === 'string') {
                        try { payload = JSON.parse(payload); } catch (_) { payload = {}; }
                    }
                    const conditionID = Number(payload && (payload.intended_condition_id || payload.condition_id)) || 0;
                    if (!conditionID || !evidenceID || linkedEvidence.has(evidenceID)) continue;
                    if (!drafts[conditionID]) drafts[conditionID] = [];
                    drafts[conditionID].push(evidenceID);
                }
                this.executionEvidenceDrafts = drafts;
                this.executionLoaded = true;
            },

            captureExecutionLeaseToken(value) {
                const response = value && typeof value === 'object' ? value : {};
                const root = response.execution && typeof response.execution === 'object' ? response.execution : response;
                const attempt = root.active_attempt || root.current_attempt || root.attempt || {};
                const token = response.lease_token || root.lease_token || attempt.lease_token;
                const attemptID = Number(attempt.id || attempt.attempt_id) || 0;
                if (attemptID && typeof token === 'string' && token.trim()) {
                    this.executionLeaseTokens = { ...this.executionLeaseTokens, [attemptID]: token.trim() };
                }
            },

            currentExecutionLeaseToken() {
                return this.executionLeaseTokens[this.executionAttemptID()] || '';
            },

            clearExecutionLeaseToken(attemptID = this.executionAttemptID()) {
                if (!attemptID || !this.executionLeaseTokens[attemptID]) return;
                const next = { ...this.executionLeaseTokens };
                delete next[attemptID];
                this.executionLeaseTokens = next;
            },

            async loadIssueExecution(workItemID = this.selectedIssue && this.selectedIssue.id) {
                if (!workItemID || this.executionAction === 'resume_work') return;
                this.executionLoading = true;
                this.executionError = '';
                const previousAction = this.executionAction;
                this.executionAction = 'resume_work';
                try {
                    const data = await this.invokeTool('resume_work', { work_item_id: Number(workItemID) });
                    this.assertExecutionToolSuccess(data);
                    this.applyIssueExecution(this.toolValue(data));
                } catch (e) {
                    this.workExecution = emptyExecution();
                    this.selectedIssueExecution = null;
                    this.executionLoaded = true;
                    this.executionError = this.executionFailureMessage(e, '실행 기록을 불러오지 못했습니다.');
                } finally {
                    this.executionLoading = false;
                    this.executionAction = previousAction;
                }
            },

            executionBusy() {
                return Boolean(this.executionAction);
            },

            assertExecutionToolSuccess(data) {
                if (data && data.error) {
                    throw new Error(data.error.message || '요청이 거절되었습니다.');
                }
                if (data && data.result && data.result.isError) {
                    const text = Array.isArray(data.result.content)
                        ? data.result.content.filter(item => item && item.type === 'text').map(item => item.text).filter(Boolean).join('\n')
                        : '';
                    throw new Error(text || '요청이 거절되었습니다.');
                }
            },

            executionFailureMessage(error, fallback = '작업을 처리하지 못했습니다.') {
                const message = String(error && error.message || '').trim();
                const lower = message.toLowerCase();
                if (lower.includes('worktree already has an active attempt')) return '이 Git 작업 공간에서 다른 작업이 진행 중입니다. 해당 작업을 끝내거나 인계한 뒤 다시 시도하세요.';
                if (lower.includes('already has an active attempt')) return '다른 에이전트가 작업 중입니다. 작업권이 끝난 뒤 다시 시도하세요.';
                if (lower.includes('lease is invalid or expired')) return '작업권이 없거나 만료되었습니다. 실행 상태를 다시 불러오세요.';
                if (lower.includes('requires at least one required completion condition')) return '필수 완료 조건을 하나 이상 입력하세요.';
                if (lower.includes('every required completion condition')) return '필수 완료 조건의 근거와 확인 상태를 점검하세요.';
                if (lower.includes('unfinished blocking work')) return '막힌 항목을 먼저 끝내세요.';
                if (lower.includes('completion condition not found') || lower.includes('work evidence not found')) return '완료 조건이나 근거가 바뀌었습니다. 다시 불러오세요.';
                if (lower.includes('completed or canceled work')) return '이미 완료되었거나 취소된 작업입니다.';
                return message ? fallback + ' ' + message : fallback;
            },

            issueExecutionRoot(item = this.selectedIssue) {
                const selected = this.selectedIssue;
                const isSelected = !item || (selected && Number(item.id) === Number(selected.id));
                const source = isSelected && this.selectedIssueExecution ? this.selectedIssueExecution : (item || {});
                if (source.execution && typeof source.execution === 'object') return source.execution;
                if (source.resume && typeof source.resume === 'object') return source.resume;
                if (source.execution_bundle && typeof source.execution_bundle === 'object') return source.execution_bundle;
                return source;
            },

            issueAttempt(item = this.selectedIssue) {
                const selected = this.selectedIssue;
                const isSelected = !item || (selected && Number(item.id) === Number(selected.id));
                if (isSelected && this.workExecution && this.workExecution.attempt) return this.workExecution.attempt;
                const root = this.issueExecutionRoot(item);
                const attempts = this.executionArray(root.attempts);
                return root.active_attempt || root.current_attempt || root.latest_attempt || root.attempt ||
                    attempts.find(candidate => candidate && !this.executionStatusClosed(candidate.status || candidate.state)) || attempts[0] || null;
            },

            hasActiveIssueAttempt(item = this.selectedIssue) {
                const selected = this.selectedIssue;
                const isSelected = !item || (selected && Number(item.id) === Number(selected.id));
                const attempt = this.issueAttempt(item);
                if (attempt) {
                    const status = String(attempt.status || attempt.state || '').toLowerCase();
                    if (this.executionStatusClosed(status)) return false;
                    return !['prepared', 'ready', 'available', 'paused', 'expired', 'handed_off', 'handoff'].includes(status);
                }
                if (isSelected && this.executionLoaded && !this.executionError) return false;
                return Boolean(item && item.status === 'doing');
            },

            issueHasExecutionFlow(item = this.selectedIssue) {
                const selected = this.selectedIssue;
                const isSelected = !item || (selected && Number(item.id) === Number(selected.id));
                if (isSelected && this.workExecution && (this.workExecution.attempt || this.workExecution.conditions.length)) return true;
                const root = this.issueExecutionRoot(item);
                const conditions = this.executionArray(root.conditions, root.required_conditions, root.completion_conditions);
                return Boolean(this.issueAttempt(item) || conditions.length);
            },

            issueCompletionRequiresFinishWork(status, item = this.selectedIssue) {
                return status === 'done' && this.issueHasExecutionFlow(item);
            },

            issueStatusGuardMessage() {
                if (this.executionError) return '실행 상태를 확인한 뒤 바꿀 수 있습니다.';
                if (this.hasActiveIssueAttempt()) return '실행 중에는 상태를 직접 바꿀 수 없습니다.';
                if (this.issueHasExecutionFlow()) return '완료 조건이 있는 작업은 ‘조건 확인 후 완료’로 끝내세요.';
                return '';
            },

            executionAttempt() {
                return this.workExecution && this.workExecution.attempt ? this.workExecution.attempt : null;
            },

            executionAttemptID() {
                const attempt = this.executionAttempt();
                return Number(attempt && (attempt.id || attempt.attempt_id)) || 0;
            },

            executionHasAttempt() {
                return Boolean(this.executionAttempt());
            },

            executionStatusClosed(status) {
                return ['finished', 'completed', 'done', 'canceled', 'cancelled'].includes(String(status || '').toLowerCase());
            },

            executionStatusValue() {
                const attempt = this.executionAttempt();
                return String(attempt && (attempt.status || attempt.state) || '').toLowerCase();
            },

            executionCanStart() {
                if (!this.workExecution.conditions.length) return false;
                if (!this.executionHasAttempt()) return true;
                return ['prepared', 'ready', 'available', 'paused', 'expired', 'handed_off', 'handoff'].includes(this.executionStatusValue());
            },

            executionCanPrepare() {
                if (!this.executionHasAttempt()) return true;
                return ['prepared', 'ready', 'available', 'paused', 'expired', 'handed_off', 'handoff'].includes(this.executionStatusValue());
            },

            executionCanRecord() {
                return this.executionHasAttempt() && Boolean(this.currentExecutionLeaseToken()) && !this.executionStatusClosed(this.executionStatusValue()) && !this.executionCanStart();
            },

            executionStatusLabel() {
                if (this.executionLoading) return '불러오는 중';
                if (this.executionError) return '확인 필요';
                if (!this.executionHasAttempt()) return this.workExecution.conditions.length ? '시작 전' : '기록 없음';
                return {
                    prepared: '시작 전', ready: '시작 전', available: '시작 가능',
                    active: '진행 중', started: '진행 중', doing: '진행 중', in_progress: '진행 중', claimed: '진행 중',
                    blocked: '막힘', paused: '멈춤', expired: '작업권 만료',
                    handed_off: '인계됨', handoff: '인계됨', finished: '완료', completed: '완료', done: '완료',
                    canceled: '취소', cancelled: '취소'
                }[this.executionStatusValue()] || '진행 중';
            },

            executionStateClass() {
                const status = this.executionStatusValue();
                if (this.executionError || status === 'blocked') return 'is-blocked';
                if (this.executionStatusClosed(status)) return 'is-finished';
                if (this.executionHasAttempt()) return 'is-active';
                return '';
            },

            executionAttemptLabel() {
                const attempt = this.executionAttempt() || {};
                const number = Number(attempt.attempt_number) || 0;
                const id = this.executionAttemptID();
                if (number && id) return number + '차 · #' + id;
                return id ? '#' + id : '번호 없음';
            },

            executionAgentLabel() {
                const attempt = this.executionAttempt() || {};
                return String(attempt.agent || attempt.agent_id || attempt.owner || attempt.claimed_by || '').trim() || '지정 안 됨';
            },

            executionWorktreeLabel() {
                const attempt = this.executionAttempt() || {};
                const worktree = attempt.worktree && typeof attempt.worktree === 'object' ? attempt.worktree : {};
                const links = Array.isArray(this.workExecution.worktreeLinks) ? this.workExecution.worktreeLinks : [];
                const linked = links.find(item => item && item.worktree && item.worktree.agent_id === attempt.agent_id) || links[0] || {};
                const linkedWorktree = linked.worktree && typeof linked.worktree === 'object' ? linked.worktree : {};
                const label = worktree.branch || worktree.worktree_path || attempt.worktree_branch || attempt.worktree_path || linkedWorktree.branch || linkedWorktree.worktree_path;
                const id = Number(worktree.id || attempt.worktree_id || linkedWorktree.id) || 0;
                return String(label || (id ? '#' + id : '')).trim() || '연결 안 됨';
            },

            executionLeaseDate() {
                const attempt = this.executionAttempt() || {};
                const raw = attempt.lease_until || attempt.lease_expires_at || attempt.lease_expires || attempt.claim_expires_at;
                if (!raw) return null;
                const date = new Date(raw);
                return Number.isNaN(date.getTime()) ? null : date;
            },

            executionLeaseExpired() {
                const date = this.executionLeaseDate();
                return Boolean(date && date.getTime() <= Date.now());
            },

            executionLeaseLabel() {
                const date = this.executionLeaseDate();
                if (!date) return '기한 없음';
                const minutes = Math.ceil((date.getTime() - Date.now()) / 60000);
                if (minutes <= 0) return '만료됨';
                if (minutes < 60) return minutes + '분 남음';
                if (minutes < 1440) return Math.ceil(minutes / 60) + '시간 남음';
                return date.toLocaleString();
            },

            checkpointSummary(checkpoint) {
                if (!checkpoint || typeof checkpoint !== 'object') return '내용이 없습니다.';
                const summary = String(checkpoint.summary || checkpoint.note || '').trim();
                const result = String(checkpoint.result || checkpoint.observed_result || '').trim();
                return [summary, result && result !== summary ? result : ''].filter(Boolean).join('\n') || '내용이 없습니다.';
            },

            checkpointTime(checkpoint) {
                return checkpoint && (checkpoint.created_at || checkpoint.checkpointed_at || checkpoint.updated_at);
            },

            formatExecutionTime(value) {
                if (!value) return '';
                const date = new Date(value);
                return Number.isNaN(date.getTime()) ? '' : date.toLocaleString();
            },

            blockerText(blocker) {
                if (typeof blocker === 'string') return blocker;
                return String(blocker && (blocker.title || blocker.message || blocker.description || blocker.summary) || '내용 없음');
            },

            conditionID(condition) {
                return Number(condition && (condition.id || condition.condition_id)) || 0;
            },

            conditionText(condition) {
                if (typeof condition === 'string') return condition;
                return String(condition && (condition.title || condition.description || condition.condition || condition.text) || '완료 조건');
            },

            conditionVerified(condition) {
                if (!condition || typeof condition !== 'object') return false;
                const status = String(condition.status || condition.state || '').toLowerCase();
                return condition.verified === true || condition.passed === true || ['verified', 'passed', 'waived', 'complete', 'completed', 'done'].includes(status);
            },

            conditionStatusLabel(condition) {
                return String(condition && condition.status || '').toLowerCase() === 'waived' ? '면제됨' : (this.conditionVerified(condition) ? '확인됨' : '확인 전');
            },

            conditionRequirementLabel(condition) {
                return condition && condition.required === false ? '선택' : '필수';
            },

            conditionEvidenceCount(condition) {
                return this.conditionEvidenceIDs(condition).length;
            },

            conditionEvidenceIDs(condition) {
                const conditionID = this.conditionID(condition);
                const linked = condition && Array.isArray(condition.evidence_ids) ? condition.evidence_ids.map(Number) : [];
                const pending = Array.isArray(this.executionEvidenceDrafts[conditionID]) ? this.executionEvidenceDrafts[conditionID].map(Number) : [];
                const combined = [...new Set([...linked, ...pending].filter(id => id > 0))];
                if (combined.length) return combined;
                const id = this.conditionID(condition);
                return this.workExecution.evidence
                    .filter(item => Number(item && (item.condition_id || item.work_condition_id)) === id)
                    .map(item => Number(item && item.id))
                    .filter(evidenceID => evidenceID > 0);
            },

            allConditionsVerified() {
                const required = this.workExecution.conditions.filter(condition => condition && condition.required !== false);
                return required.length > 0 && required.every(condition => this.conditionVerified(condition));
            },

            pendingExecutionConditions() {
                return this.workExecution.conditions.filter(condition => !this.conditionVerified(condition));
            },

            executionCanFinish() {
                return this.executionCanRecord() && this.allConditionsVerified() && this.workExecution.blockers.length === 0;
            },

            finishWorkHint() {
                if (this.workExecution.blockers.length) return '막힌 항목을 먼저 끝내세요.';
                if (!this.allConditionsVerified()) return '필수 완료 조건과 근거를 먼저 확인하세요.';
                return '확인된 결과로 작업을 완료합니다.';
            },

            evidenceSummary(evidence) {
                return String(evidence && (evidence.summary || evidence.content || evidence.result || evidence.description) || '근거');
            },

            evidenceMeta(evidence) {
                if (!evidence || typeof evidence !== 'object') return '';
                const kind = {
                    test: '실행 확인', observation: '화면 확인', artifact: '결과물', review: '검토'
                }[evidence.kind || evidence.evidence_type] || evidence.kind || evidence.evidence_type || '';
                const conditionIDs = Array.isArray(evidence.condition_ids)
                    ? evidence.condition_ids.map(Number)
                    : [Number(evidence.condition_id || evidence.work_condition_id) || 0];
                const condition = this.workExecution.conditions.find(item => conditionIDs.includes(this.conditionID(item)));
                const time = this.formatExecutionTime(evidence.created_at || evidence.submitted_at);
                const reference = evidence.reference || evidence.url || evidence.path || '';
                return [kind, condition ? this.conditionText(condition) : '', reference, time].filter(Boolean).join(' · ');
            },

            executionActor() {
                const attempt = this.executionAttempt() || {};
                const current = attempt.agent || attempt.agent_id || attempt.owner || '';
                const planActor = typeof this.planActorName === 'function' ? this.planActorName() : '';
                return String(current || planActor || (this.auth && this.auth.user) || 'codex').trim();
            },

            executionWorktreeID() {
                const attempt = this.executionAttempt() || {};
                const current = Number(attempt.worktree_id || (attempt.worktree && attempt.worktree.id)) || 0;
                if (current) return current;
                const links = Array.isArray(this.workExecution.worktreeLinks) ? this.workExecution.worktreeLinks : [];
                const linked = links.find(item => item && item.worktree && item.worktree.agent_id === attempt.agent_id) || links[0];
                const linkedID = Number(linked && linked.worktree && linked.worktree.id) || 0;
                if (linkedID) return linkedID;
                const linkedIDs = this.selectedIssue && Array.isArray(this.selectedIssue.worktree_ids) ? this.selectedIssue.worktree_ids : [];
                return Number(linkedIDs[0]) || 0;
            },

            conditionVerificationText(condition) {
                let verification = condition && condition.verification;
                if (typeof verification === 'string') {
                    try { verification = JSON.parse(verification); } catch (_) { return verification.trim(); }
                }
                if (!verification || typeof verification !== 'object') return '';
                const value = verification.instructions || verification.command || verification.path || verification.url || verification.selector || verification.expected;
                return typeof value === 'string' ? value.trim() : '';
            },

            addExecutionCondition() {
                this.executionForm.prepareConditions.push(emptyPrepareCondition());
            },

            removeExecutionCondition(index) {
                if (this.executionForm.prepareConditions.length <= 1) return;
                this.executionForm.prepareConditions.splice(index, 1);
            },

            openExecutionForm(mode) {
                const pendingCondition = this.workExecution.conditions.find(condition => !this.conditionVerified(condition));
                const prepareConditions = this.workExecution.conditions.length
                    ? this.workExecution.conditions.map(condition => ({
                        kind: String(condition && condition.kind || 'custom'),
                        description: this.conditionText(condition),
                        verification: this.conditionVerificationText(condition)
                    }))
                    : [emptyPrepareCondition()];
                this.executionForm = {
                    ...emptyForm(),
                    agent: this.executionActor(),
                    worktreeId: this.executionWorktreeID() || '',
                    nextAction: this.workExecution.nextAction || '',
                    prepareConditions,
                    conditionId: pendingCondition ? this.conditionID(pendingCondition) : ''
                };
                this.executionFormMode = mode;
                this.executionError = '';
            },

            closeExecutionForm() {
                this.executionFormMode = '';
                this.executionForm = emptyForm();
            },

            executionMutationArgs(extra = {}, includeAttempt = true) {
                const args = { ...extra };
                if (includeAttempt) {
                    const attemptID = this.executionAttemptID();
                    if (attemptID) args.attempt_id = attemptID;
                } else {
                    args.work_item_id = Number(this.selectedIssue && this.selectedIssue.id);
                }
                return args;
            },

            async runExecutionMutation(toolName, args, successMessage) {
                if (this.executionBusy()) return false;
                this.executionAction = toolName;
                this.executionError = '';
                const fingerprint = toolName + ':' + JSON.stringify(args);
                const pending = this.executionPendingMutation;
                const actionKey = pending && pending.fingerprint === fingerprint
                    ? pending.actionKey
                    : window.crypto.randomUUID();
                this.executionPendingMutation = { fingerprint, actionKey };
                const requestArgs = { ...args, action_key: actionKey };
                const leaseToken = this.currentExecutionLeaseToken();
                if (leaseToken && !['prepare_work', 'claim_work'].includes(toolName)) {
                    requestArgs.lease_token = leaseToken;
                }
                try {
                    const data = await this.invokeTool(toolName, requestArgs);
                    this.assertExecutionToolSuccess(data);
                    const value = this.toolValue(data);
                    if (value && typeof value === 'object') {
                        this.captureExecutionLeaseToken(value);
                        this.applyIssueExecution(value);
                    }
                    await this.loadIssueExecution(this.selectedIssue && this.selectedIssue.id);
                    this.executionPendingMutation = null;
                    this.setNotice(successMessage);
                    return true;
                } catch (e) {
                    this.executionError = this.executionFailureMessage(e, '실행 상태를 저장하지 못했습니다.');
                    this.setNotice('실행 상태를 저장하지 못했습니다.', 'error', 0);
                    return false;
                } finally {
                    this.executionAction = '';
                }
            },

            async prepareWork() {
                const conditions = this.executionForm.prepareConditions.map(condition => ({
                    kind: String(condition && condition.kind || 'custom').trim() || 'custom',
                    description: String(condition && condition.description || '').trim(),
                    verification: String(condition && condition.verification || '').trim()
                })).filter(condition => condition.description || condition.verification);
                const nextAction = String(this.executionForm.nextAction || '').trim();
                if (!conditions.length || conditions.some(condition => !condition.description || !condition.verification) || !nextAction) return;
                const saved = await this.runExecutionMutation('prepare_work', this.executionMutationArgs({
                    next_action: nextAction,
                    conditions: conditions.map(condition => ({
                        kind: condition.kind,
                        description: condition.description,
                        verification: { instructions: condition.verification },
                        required: true
                    }))
                }, false), '완료 조건을 저장했습니다.');
                if (saved) this.closeExecutionForm();
            },

            async claimWork() {
                const args = this.executionMutationArgs({
                    agent_id: String(this.executionForm.agent || '').trim(),
                    lease_seconds: Math.max(5, Number(this.executionForm.leaseMinutes) || 30) * 60
                }, false);
                const worktreeID = Number(this.executionForm.worktreeId) || 0;
                if (worktreeID) args.worktree_id = worktreeID;
                const saved = await this.runExecutionMutation('claim_work', args, '작업을 시작했습니다.');
                if (saved) this.closeExecutionForm();
            },

            async checkpointWork() {
                const saved = await this.runExecutionMutation('checkpoint_work', this.executionMutationArgs({
                    summary: String(this.executionForm.summary || '').trim(),
                    result: String(this.executionForm.result || '').trim(),
                    next_action: String(this.executionForm.nextAction || '').trim(),
                    lease_seconds: Math.max(5, Number(this.executionForm.leaseMinutes) || 30) * 60
                }), '중간 기록을 저장했습니다.');
                if (saved) this.closeExecutionForm();
            },

            async submitWorkEvidence() {
                const conditionID = Number(this.executionForm.conditionId) || 0;
                if (!conditionID) return;
                const saved = await this.runExecutionMutation('submit_work_evidence', this.executionMutationArgs({
                    evidence_type: this.executionForm.evidenceKind,
                    summary: String(this.executionForm.evidenceSummary || '').trim(),
                    reference: String(this.executionForm.evidenceReference || '').trim(),
                    payload: { intended_condition_id: conditionID },
                    condition_ids: [conditionID]
                }), '근거를 추가했습니다.');
                if (saved) this.closeExecutionForm();
            },

            async verifyWorkCondition(condition) {
                if (this.conditionEvidenceCount(condition) < 1) return;
                const saved = await this.runExecutionMutation('verify_work_condition', this.executionMutationArgs({
                    condition_id: this.conditionID(condition),
                    status: 'passed',
                    evidence_ids: this.conditionEvidenceIDs(condition)
                }), '완료 조건을 확인했습니다.');
                if (saved) {
                    const drafts = { ...this.executionEvidenceDrafts };
                    delete drafts[this.conditionID(condition)];
                    this.executionEvidenceDrafts = drafts;
                }
            },

            async handoffWork() {
                const attemptID = this.executionAttemptID();
                const saved = await this.runExecutionMutation('handoff_work', this.executionMutationArgs({
                    summary: String(this.executionForm.summary || '').trim(),
                    result: String(this.executionForm.result || '').trim(),
                    next_action: String(this.executionForm.nextAction || '').trim()
                }), '작업을 인계했습니다.');
                if (saved) {
                    this.clearExecutionLeaseToken(attemptID);
                    this.closeExecutionForm();
                }
            },

            async finishWork() {
                if (!this.executionCanFinish()) return;
                if (!window.confirm('모든 완료 조건이 확인되었습니다. 작업을 완료할까요?')) return;
                const attemptID = this.executionAttemptID();
                const saved = await this.runExecutionMutation('finish_work', this.executionMutationArgs({
                    summary: '완료 조건 확인',
                    result: `필수 완료 조건 ${this.workExecution.conditions.filter(condition => condition && condition.required !== false).length}개 확인`
                }), '작업을 완료했습니다.');
                if (saved) this.clearExecutionLeaseToken(attemptID);
            }
        };
    }


    return { createIssueExecutionViewModel };
}));
