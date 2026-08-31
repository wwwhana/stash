(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashWorkBoardScopeViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const text = value => String(value || '').trim();

    function createWorkBoardScopeViewModel() {
        return {
            boardProjectSlug: '',
            boardNamespaceSlug: '',

            boardProjects() {
                return (this.mapNamespaces || []).filter(item => /^\/projects\/[^/]+$/.test(text(item && item.slug)));
            },

            boardNamespaces() {
                const project = text(this.boardProjectSlug);
                if (!project) return this.mapNamespaces || [];
                return (this.mapNamespaces || []).filter(item => {
                    const slug = text(item && item.slug);
                    return slug !== project && slug.startsWith(project + '/');
                });
            },

            syncBoardProjectFromNamespace() {
                const projects = this.boardProjects();
                const selectedProject = text(this.boardProjectSlug);
                const selectedNamespace = text(this.boardNamespaceSlug);
                if (!this.mapNamespacesLoaded && (selectedProject || selectedNamespace)) return;
                if (selectedProject && projects.some(item => item.slug === selectedProject)) {
                    if (selectedNamespace === selectedProject) this.boardNamespaceSlug = '';
                    if (selectedNamespace && !selectedNamespace.startsWith(selectedProject + '/')) this.boardNamespaceSlug = '';
                    return;
                }
                const project = projects.find(item => selectedNamespace === item.slug || selectedNamespace.startsWith(item.slug + '/'));
                this.boardProjectSlug = project ? project.slug : '';
                if (project && selectedNamespace === project.slug) this.boardNamespaceSlug = '';
            },

            workBoardNamespacesArgument() {
                const namespace = text(this.boardNamespaceSlug);
                if (namespace) return namespace;
                const project = text(this.boardProjectSlug);
                return project || '/';
            },

            workBoardCreationNamespace() {
                return text(this.boardNamespaceSlug) || text(this.boardProjectSlug) || '/';
            },

            workBoardScopeLabel() {
                const slug = text(this.boardNamespaceSlug) || text(this.boardProjectSlug);
                if (!slug) return '모든 범위';
                const item = (this.mapNamespaces || []).find(candidate => text(candidate && candidate.slug) === slug);
                return item ? item.label : slug;
            },

            async changeWorkBoardProject() {
                const project = text(this.boardProjectSlug);
                const namespace = text(this.boardNamespaceSlug);
                if (!project || namespace === project || !namespace.startsWith(project + '/')) this.boardNamespaceSlug = '';
                await this.loadWorkBoard();
            },

            async changeWorkBoardNamespace() {
                this.syncBoardProjectFromNamespace();
                await this.loadWorkBoard();
            }
        };
    }

    return { createWorkBoardScopeViewModel };
}));
