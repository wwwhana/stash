(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashWorkMonitorViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createWorkMonitorViewModel() {
        let requestSequence = 0;
        const list = (...values) => values.find(value => Array.isArray(value) && value.length) || values.find(Array.isArray) || [];
        const text = value => String(value || '').trim();
        const firstObject = values => values.find(value => value && typeof value === 'object') || null;
        const inputBytes = value => {
            const serialized = JSON.stringify(value || {});
            if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(serialized).length;
            return encodeURIComponent(serialized).replace(/%[0-9A-F]{2}|./g, 'x').length;
        };

        return {
            graphProjectSlug: '',
            workMonitorWorkID: 0,
            workMonitorBrief: null,
            workMonitorLoading: false,
            workMonitorError: '',

            graphProjects() {
                return (this.mapNamespaces || []).filter(item => /^\/projects\/[^/]+$/.test(text(item && item.slug)));
            },

            graphNamespaces() {
                const project = text(this.graphProjectSlug);
                if (!project) return this.mapNamespaces || [];
                return (this.mapNamespaces || []).filter(item => {
                    const slug = text(item && item.slug);
                    return slug !== project && slug.startsWith(project + '/');
                });
            },

            syncGraphProjectFromNamespace() {
                const projects = this.graphProjects();
                const selected = text(this.graphProjectSlug);
                const namespace = text(this.mapNamespaceSlug);
                if (selected && projects.some(item => item.slug === selected)) {
                    if (namespace === selected) this.mapNamespaceSlug = '';
                    return;
                }
                const project = projects.find(item => namespace === item.slug || namespace.startsWith(item.slug + '/'));
                this.graphProjectSlug = project ? project.slug : '';
                if (project && namespace === project.slug) this.mapNamespaceSlug = '';
            },

            async changeWorkGraphProject() {
                const project = text(this.graphProjectSlug);
                const namespace = text(this.mapNamespaceSlug);
                if (!project || namespace === project || !namespace.startsWith(project + '/')) this.mapNamespaceSlug = '';
                this.graphFocusedKey = '';
                this.clearWorkMonitor();
                await this.loadWorkGraph(false);
            },

            graphAgents() {
                const agents = new Set((this.graph && this.graph.nodes || []).map(item => (
                    text(item && (item.agent_id || item.owner))
                )).filter(Boolean));
                return Array.from(agents).sort((left, right) => left.localeCompare(right, 'ko'));
            },

            clearWorkMonitor() {
                requestSequence += 1;
                this.workMonitorWorkID = 0;
                this.workMonitorBrief = null;
                this.workMonitorLoading = false;
                this.workMonitorError = '';
            },

            async loadWorkMonitor(workItemID, force = false) {
                const id = Number(workItemID) || 0;
                if (!id) {
                    this.clearWorkMonitor();
                    return;
                }
                if (!force && this.workMonitorWorkID === id && this.workMonitorBrief) return;
                const sequence = ++requestSequence;
                this.workMonitorWorkID = id;
                this.workMonitorBrief = null;
                this.workMonitorError = '';
                this.workMonitorLoading = true;
                try {
                    const response = await this.invokeTool('resume_work', { work_item_id: id, detail: 'brief' });
                    const brief = this.toolValue(response);
                    if (sequence !== requestSequence || this.workMonitorWorkID !== id) return;
                    this.workMonitorBrief = brief && typeof brief === 'object' ? brief : {};
                } catch (error) {
                    if (sequence !== requestSequence || this.workMonitorWorkID !== id) return;
                    this.workMonitorError = text(error && error.message) || '작업 현황을 불러오지 못했습니다.';
                } finally {
                    if (sequence === requestSequence && this.workMonitorWorkID === id) this.workMonitorLoading = false;
                }
            },

            selectedWorkMonitor() {
                const brief = this.workMonitorBrief && typeof this.workMonitorBrief === 'object' ? this.workMonitorBrief : {};
                const focused = typeof this.graphFocusedItem === 'function' ? this.graphFocusedItem() : null;
                const item = firstObject([brief.work_item, focused]) || {};
                const attempt = firstObject([brief.latest_attempt, brief.active_attempt, brief.current_attempt]) || {};
                const checkpoint = firstObject([brief.latest_checkpoint, brief.last_checkpoint]) || {};
                const goalContext = brief.goal_context && typeof brief.goal_context === 'object' ? brief.goal_context : {};
                const path = list(goalContext.path, brief.goal_path);
                const sharedGoal = firstObject([brief.shared_goal, path[0]]) || null;
                const blockers = list(brief.blockers, checkpoint.blockers, attempt.blockers);
                const evidence = list(brief.evidence_references, brief.recent_evidence, brief.evidence);
                const evidenceItem = firstObject(evidence) || {};
                const windowState = brief.context_window && typeof brief.context_window === 'object' ? brief.context_window : {};
                const exactBytes = Number(windowState.input_bytes) || 0;
                return {
                    item,
                    sharedGoal,
                    goalPath: path,
                    agent: text(attempt.agent_id || attempt.agent || item.agent_id || item.owner) || '지정 안 됨',
                    status: text(item.status || attempt.status),
                    blocker: blockers.length ? this.monitorBlockerText(blockers[0]) : '',
                    result: text(checkpoint.result || checkpoint.observed_result || checkpoint.summary),
                    evidence: text(evidenceItem.summary || evidenceItem.title),
                    evidenceReference: text(evidenceItem.reference || evidenceItem.uri || evidenceItem.content_digest),
                    inputBytes: exactBytes || inputBytes(brief),
                    inputLimitBytes: Number(windowState.input_limit_bytes) || 0,
                    inputEstimated: !exactBytes,
                    truncated: windowState.truncated === true
                };
            },

            monitorBlockerText(blocker) {
                if (typeof blocker === 'string') return text(blocker);
                return text(blocker && (blocker.message || blocker.title || blocker.summary || blocker.description));
            },

            monitorGoalText(goal) {
                return text(goal && (goal.content || goal.title || goal.name));
            },

            monitorInputLabel(monitor = this.selectedWorkMonitor()) {
                const format = bytes => bytes >= 1024 ? `${(bytes / 1024).toFixed(bytes >= 10240 ? 0 : 1)} KB` : `${bytes} B`;
                const current = format(Number(monitor && monitor.inputBytes) || 0);
                const limit = Number(monitor && monitor.inputLimitBytes) || 0;
                return `${monitor && monitor.inputEstimated ? '약 ' : ''}${current}${limit ? ' / ' + format(limit) : ''}${monitor && monitor.truncated ? ' · 일부 표시' : ''}`;
            }
        };
    }

    return { createWorkMonitorViewModel };
}));
