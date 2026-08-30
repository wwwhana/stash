(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashWorkPlanViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createWorkPlanViewModel() {
        const emptyPlan = () => ({ goal_tree: { root_goal_id: null, goals: [] }, components: [], decisions: [], warnings: [], validation: null });
        const emptyComponentForm = () => ({ componentId: null, goalId: '', title: '', description: '', technicalDetails: '', ownedScopes: '', priority: 0 });
        const emptyTaskForm = () => ({ taskId: null, componentId: null, goalId: '', title: '', description: '', technicalDetails: '', provenance: 'agent' });
        const emptyDecisionForm = () => ({ componentId: '', title: '', rationale: '' });
        return {
            plan: emptyPlan(),
            planNamespaceSlug: '',
            planLoaded: false,
            planError: '',
            planActor: '',
            planComponentFormOpen: false,
            planTaskFormOpen: false,
            planDecisionFormOpen: false,
            planScopeEditorComponentId: null,
            planScopeEditorValue: '',
            planComponentForm: emptyComponentForm(),
            planTaskForm: emptyTaskForm(),
            planDecisionForm: emptyDecisionForm(),

            planProjects() {
                return this.mapNamespaces.filter(item => /^\/projects\/[^/]+$/.test(String(item && item.slug || '')));
            },

            ensurePlanNamespace() {
                const projects = this.planProjects();
                if (projects.some(item => item.slug === this.planNamespaceSlug)) return;
                const mapProject = projects.find(item => item.slug === this.mapNamespaceSlug);
                this.planNamespaceSlug = mapProject ? mapProject.slug : (projects[0] && projects[0].slug || '');
            },

            planNamespace() {
                return this.planNamespaceSlug;
            },

            planScopeLabel() {
                const namespace = this.planProjects().find(item => item.slug === this.planNamespaceSlug);
                return namespace ? namespace.label : this.planNamespaceSlug;
            },

            resetPlanEditors() {
                this.planComponentFormOpen = false;
                this.planTaskFormOpen = false;
                this.planDecisionFormOpen = false;
                this.planScopeEditorComponentId = null;
                this.planScopeEditorValue = '';
                this.planComponentForm = emptyComponentForm();
                this.planTaskForm = emptyTaskForm();
                this.planDecisionForm = emptyDecisionForm();
            },

            async fetchWorkPlan() {
                const namespace = this.planNamespace();
                if (!namespace) return;
                const data = await this.invokeTool('get_work_plan', { namespace });
                const value = this.toolValue(data) || {};
                this.plan = {
                    goal_tree: value.goal_tree && typeof value.goal_tree === 'object' ? {
                        root_goal_id: value.goal_tree.root_goal_id === undefined ? null : value.goal_tree.root_goal_id,
                        goals: Array.isArray(value.goal_tree.goals) ? value.goal_tree.goals : []
                    } : { root_goal_id: null, goals: [] },
                    components: Array.isArray(value.components) ? value.components : [],
                    decisions: Array.isArray(value.decisions) ? value.decisions : [],
                    warnings: Array.isArray(value.warnings) ? value.warnings : [],
                    validation: value.validation && typeof value.validation === 'object' ? value.validation : null
                };
                this.planLoaded = true;
                this.planError = '';
                this.markLoaded();
            },

            async loadWorkPlan(refreshNamespaces = true) {
                this.activeNav = 'plan';
                this.resultTitle = '작업 계획';
                this.resultDescription = '프로젝트의 구성 요소와 할 일을 관리합니다.';
                this.view = 'plan';
                this.loading = true;
                this.planLoaded = false;
                this.planError = '';
                this.plan = emptyPlan();
                this.resetPlanEditors();
                try {
                    await this.loadMapNamespaces(refreshNamespaces);
                    if (this.mapNamespaceError) {
                        this.planError = '프로젝트 목록을 불러오지 못했습니다.';
                        return;
                    }
                    this.ensurePlanNamespace();
                    if (!this.planNamespace()) return;
                    this.resultDescription = this.planScopeLabel() + '의 구성 요소와 할 일을 관리합니다.';
                    await this.fetchWorkPlan();
                    this.setNotice('', 'success', 0);
                } catch (e) {
                    this.plan = emptyPlan();
                    this.planLoaded = false;
                    this.planError = '작업 계획을 불러오지 못했습니다.';
                    this.setNotice('작업 계획을 불러오지 못했습니다.', 'error', 0);
                } finally {
                    this.loading = false;
                    this.syncRoute();
                }
            },

            async runPlanMutation(action, successMessage, errorMessage) {
                if (!this.planNamespace()) {
                    this.setNotice('프로젝트를 선택하세요.', 'error', 0);
                    return;
                }
                this.loading = true;
                try {
                    await action();
                    await this.fetchWorkPlan();
                    this.setNotice(successMessage);
                } catch (e) {
                    this.setNotice(errorMessage, 'error', 0);
                } finally {
                    this.loading = false;
                }
            },

            planActorName() {
                return String(this.planActor || '').trim();
            },

            planComponentTitle(componentID) {
                const id = Number(componentID);
                const component = this.plan.components.find(candidate => Number(candidate.id) === id);
                return component ? component.issue_key + ' · ' + component.title : '구성 요소를 선택하세요';
            },

				planGoals() {
					return this.plan && this.plan.goal_tree && Array.isArray(this.plan.goal_tree.goals) ? this.plan.goal_tree.goals : [];
				},

				planRootGoal() {
					const rootID = this.plan && this.plan.goal_tree && this.plan.goal_tree.root_goal_id;
					return this.planGoals().find(goal => Number(goal.id) === Number(rootID)) || null;
				},

				planGoalLabel(goal) {
					const depth = Math.max(0, Number(goal && goal.depth) || 0);
					return (depth ? '↳ '.repeat(depth) : '') + String(goal && goal.content || '목표');
				},

				planGoalShortLabel(goalID) {
					const goal = this.planGoals().find(candidate => Number(candidate.id) === Number(goalID));
					if (!goal) return '#' + goalID;
					const content = String(goal.content || '목표');
					return content.length > 32 ? content.slice(0, 31) + '…' : content;
				},

				planProgressPercent(value) {
					return Math.round(Math.max(0, Math.min(1, Number(value) || 0)) * 100) + '%';
				},

            planTaskCount(status) {
                return this.plan.components.reduce((count, component) => count + (Array.isArray(component.tasks) ? component.tasks.filter(task => task.status === status).length : 0), 0);
            },

            planProvenanceLabel(value) {
                return { agent: '에이전트', roadmap: '계획 문서' }[value] || value;
            },

            planWarningText(warning) {
                const component = this.plan.components.find(candidate => Number(candidate.id) === Number(warning.component_id));
                const task = this.plan.components.flatMap(candidate => candidate.tasks || []).find(candidate => Number(candidate.id) === Number(warning.task_id));
                return {
                    no_components: '구성 요소를 먼저 만드세요.',
                    component_without_paths: `${component ? component.title : '구성 요소'}에 맡는 범위가 없습니다.`,
                    open_task_without_provenance: `${task ? task.title : '작업'}의 출처를 기록하세요.`,
	                        active_task_without_starter: `${task ? task.title : '작업'}의 시작 작업자가 없습니다.`,
						no_project_goal: '공통 목표를 먼저 정하세요.',
						component_without_goal: `${component ? component.title : '구성 요소'}를 목표에 연결하세요.`,
						component_goal_outside_tree: `${component ? component.title : '구성 요소'}의 연결 목표를 확인하세요.`,
						task_without_goal: `${task ? task.title : '작업'}을 목표에 연결하세요.`,
						task_goal_outside_tree: `${task ? task.title : '작업'}의 연결 목표를 확인하세요.`
                }[warning.code] || '계획 내용을 확인하세요.';
            },

	                planDecisionMeta(decision) {
                const parts = [];
                if (decision.author) parts.push(decision.author);
                if (decision.created_at) {
                    const date = new Date(decision.created_at);
                    if (!Number.isNaN(date.getTime())) parts.push(date.toLocaleString());
                }
	                    return parts.join(' · ') || '기록됨';
	                },

	                planValidationStatus() {
	                    const validation = this.plan.validation;
	                    if (!validation) return '';
	                    if (validation.stale) return '다시 검사';
	                    return validation.passed ? '문제 없음' : '확인 필요';
	                },

	                planValidationMeta() {
	                    const validation = this.plan.validation;
	                    if (!validation || !validation.checked_at) return '';
	                    const date = new Date(validation.checked_at);
	                    return Number.isNaN(date.getTime()) ? '' : date.toLocaleString();
	                },

	                planValidationTarget(finding) {
	                    if (!finding) return '';
	                    const names = [];
	                    const component = this.plan.components.find(candidate => Number(candidate.id) === Number(finding.component_id));
	                    const related = this.plan.components.find(candidate => Number(candidate.id) === Number(finding.related_component_id));
	                    const task = this.plan.components.flatMap(candidate => candidate.tasks || []).find(candidate => Number(candidate.id) === Number(finding.task_id));
	                    if (component) names.push(component.issue_key + ' · ' + component.title);
	                    if (related) names.push(related.issue_key + ' · ' + related.title);
	                    if (task) names.push(task.title);
	                    return names.join(' / ');
	                },

	                async validateWorkPlan() {
	                    await this.runPlanMutation(
	                        () => this.invokeTool('validate_work_plan', { namespace: this.planNamespace() }),
	                        '계획을 검사했습니다.',
	                        '계획을 검사하지 못했습니다.'
	                    );
	                },

	                openPlanComponentForm(component = null) {
	                    this.planTaskFormOpen = false;
	                    this.planComponentForm = component ? {
	                        componentId: Number(component.id),
	                        goalId: component.goal_id || '',
	                        title: component.title || '',
	                        description: component.description || '',
	                        technicalDetails: component.technical_details || '',
	                        ownedScopes: Array.isArray(component.owned_paths) ? component.owned_paths.join(', ') : '',
	                        priority: Number(component.priority) || 0
	                    } : emptyComponentForm();
	                    this.planComponentFormOpen = true;
	                },

	                closePlanComponentForm() {
	                    this.planComponentFormOpen = false;
	                    this.planComponentForm = emptyComponentForm();
	                },

	                async savePlanComponent() {
	                    const form = this.planComponentForm;
	                    const componentID = Number(form.componentId);
	                    const goalID = Number(form.goalId) || 0;
	                    await this.runPlanMutation(async () => {
	                        if (componentID) {
	                            const args = {
	                                component_id: componentID, title: form.title, description: form.description,
	                                technical_details: form.technicalDetails, owned_paths: form.ownedScopes
	                            };
	                            if (goalID) args.goal_id = goalID;
	                            await this.invokeTool('update_plan_component', args);
	                        } else {
	                            const args = {
	                                namespace: this.planNamespace(), title: form.title, description: form.description,
	                                technical_details: form.technicalDetails, owned_paths: form.ownedScopes,
	                                priority: Number(form.priority) || 0, owner: this.planActorName()
	                            };
	                            if (goalID) args.goal_id = goalID;
	                            await this.invokeTool('create_plan_component', args);
	                        }
	                        this.closePlanComponentForm();
	                    }, componentID ? '구성 요소를 수정했습니다.' : '구성 요소를 만들었습니다.', componentID ? '구성 요소를 수정하지 못했습니다.' : '구성 요소를 만들지 못했습니다.');
	                },

	                openPlanTaskForm(componentID, task = null) {
	                    this.planComponentFormOpen = false;
	                    const component = this.plan.components.find(candidate => Number(candidate.id) === Number(componentID));
	                    this.planTaskForm = task ? {
	                        taskId: Number(task.id), componentId: Number(componentID),
	                        goalId: task.goal_id || '',
	                        title: task.title || '', description: task.description || '',
	                        technicalDetails: task.technical_details || '', provenance: task.provenance || ''
	                    } : { ...emptyTaskForm(), componentId: Number(componentID), goalId: component && component.goal_id || '' };
	                    this.planTaskFormOpen = true;
	                },

	                closePlanTaskForm() {
	                    this.planTaskFormOpen = false;
	                    this.planTaskForm = emptyTaskForm();
	                },

	                async savePlanTask() {
	                    const form = this.planTaskForm;
	                    const taskID = Number(form.taskId);
	                    const componentID = Number(form.componentId);
	                    const goalID = Number(form.goalId) || 0;
	                    if (!componentID) {
	                        this.setNotice('작업을 넣을 구성 요소를 선택하세요.', 'error', 0);
	                        return;
	                    }
	                    await this.runPlanMutation(async () => {
	                        if (taskID) {
	                            const args = {
	                                task_id: taskID, title: form.title, description: form.description,
	                                technical_details: form.technicalDetails, provenance: form.provenance
	                            };
	                            if (goalID) args.goal_id = goalID;
	                            await this.invokeTool('update_plan_task', args);
	                        } else {
	                            const args = {
	                                namespace: this.planNamespace(), component_id: componentID, title: form.title, description: form.description,
	                                technical_details: form.technicalDetails, provenance: form.provenance
	                            };
	                            if (goalID) args.goal_id = goalID;
	                            await this.invokeTool('create_plan_task', args);
	                        }
	                        this.closePlanTaskForm();
	                    }, taskID ? '작업을 수정했습니다.' : '작업을 만들었습니다.', taskID ? '작업을 수정하지 못했습니다.' : '작업을 만들지 못했습니다.');
	                },

            async startPlanTask(taskID) {
                const agent = this.planActorName();
                if (!agent) {
                    this.setNotice('작업자를 입력하세요.', 'error', 0);
                    return;
                }
                await this.runPlanMutation(() => this.invokeTool('start_plan_task', { task_id: taskID, agent }), '작업을 시작했습니다.', '작업을 시작하지 못했습니다.');
            },

            async completePlanTask(taskID) {
                const agent = this.planActorName();
                if (!agent) {
                    this.setNotice('작업자를 입력하세요.', 'error', 0);
                    return;
                }
                await this.runPlanMutation(() => this.invokeTool('complete_plan_task', { task_id: taskID, agent }), '작업을 완료로 표시했습니다.', '작업을 완료로 표시하지 못했습니다.');
            },

            async blockPlanTask(taskID) {
                await this.runPlanMutation(() => this.invokeTool('block_plan_task', { task_id: taskID, agent: this.planActorName() }), '작업을 막힘으로 표시했습니다.', '작업 상태를 바꾸지 못했습니다.');
            },

            async unblockPlanTask(taskID) {
                await this.runPlanMutation(() => this.invokeTool('unblock_plan_task', { task_id: taskID }), '막힘을 해제했습니다.', '막힘을 해제하지 못했습니다.');
            },

            openPlanScopeEditor(component) {
                this.planScopeEditorComponentId = Number(component.id);
                this.planScopeEditorValue = Array.isArray(component.owned_paths) ? component.owned_paths.join(', ') : '';
            },

            closePlanScopeEditor() {
                this.planScopeEditorComponentId = null;
                this.planScopeEditorValue = '';
            },

            async savePlanComponentScopes(componentID) {
                await this.runPlanMutation(async () => {
                    await this.invokeTool('set_plan_component_paths', { component_id: componentID, owned_paths: this.planScopeEditorValue });
                    this.closePlanScopeEditor();
                }, '맡는 범위를 저장했습니다.', '맡는 범위를 저장하지 못했습니다.');
            },

            async recordPlanDecision() {
                const form = this.planDecisionForm;
                const componentID = Number(form.componentId);
                await this.runPlanMutation(async () => {
	                        const args = { namespace: this.planNamespace(), title: form.title, rationale: form.rationale, author: this.planActorName() };
                    if (componentID) args.component_id = componentID;
                    await this.invokeTool('record_plan_decision', args);
                    this.planDecisionForm = emptyDecisionForm();
                    this.planDecisionFormOpen = false;
                }, '결정을 기록했습니다.', '결정을 기록하지 못했습니다.');
            }
        };
    }


    return { createWorkPlanViewModel };
}));
