(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashMapScopeViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function createMapScopeViewModel() {
        return {
            mapNamespaces: [],
            mapNamespaceSlug: '',
            mapNamespacesLoaded: false,
            mapNamespacesLoading: false,
            mapNamespaceError: '',

            async loadMapNamespaces(force = false) {
                if (this.mapNamespacesLoaded && !force) return;
                const chooseOnlyProject = !this.mapNamespacesLoaded && !this.mapNamespaceSlug;
                this.mapNamespacesLoading = true;
                this.mapNamespaceError = '';
                try {
                    const listed = [];
                    const limit = 100;
                    let offset = 0;
                    for (let page = 0; page < 100; page += 1) {
                        const data = await this.invokeTool('list_namespaces', { limit: limit + 1, offset });
                        const value = this.toolValue(data);
                        const result = this.pageSlice(value, limit, offset);
                        listed.push(...result.items);
                        if (!result.hasMore || !result.items.length || result.nextOffset <= offset) break;
                        offset = result.nextOffset;
                    }
                    const namespaces = new Map();
                    for (const item of listed) {
                        const rawSlug = String(item && item.slug || '').trim();
                        const slug = (rawSlug.startsWith('/') ? rawSlug : '/' + rawSlug).replace(/\/+$/, '') || '/';
                        if (slug === '/') continue;
                        const name = String(item && item.name || '').trim();
                        namespaces.set(slug, { slug, label: name && name !== slug ? `${name} · ${slug}` : slug });
                    }
                    this.mapNamespaces = Array.from(namespaces.values()).sort((left, right) => left.slug.localeCompare(right.slug, 'ko'));
                    if (this.mapNamespaceSlug && !this.mapNamespaces.some(item => item.slug === this.mapNamespaceSlug)) {
                        this.mapNamespaceSlug = '';
                    }
                    if (chooseOnlyProject && !this.mapNamespaceSlug) {
                        const projects = this.mapNamespaces.filter(item => /^\/projects\/[^/]+$/.test(item.slug));
                        if (projects.length === 1) this.mapNamespaceSlug = projects[0].slug;
                    }
                    this.mapNamespacesLoaded = true;
                } catch (_) {
                    this.mapNamespaces = [];
                    this.mapNamespaceError = '네임스페이스 목록을 불러오지 못했습니다.';
                    this.mapNamespacesLoaded = true;
                } finally {
                    this.mapNamespacesLoading = false;
                }
            }
        };
    }


    return { createMapScopeViewModel };
}));
