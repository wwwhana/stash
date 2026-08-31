(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashSearch = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    function normalizeSearchText(value) {
        return String(value === undefined || value === null ? '' : value)
            .normalize('NFKC')
            .toLocaleLowerCase('ko-KR')
            .trim();
    }

    function searchTokens(query) {
        return Array.from(new Set(normalizeSearchText(query).split(/\s+/).filter(Boolean)));
    }

    function searchableValues(values) {
        const result = [];
        const visit = value => {
            if (Array.isArray(value)) {
                value.forEach(visit);
                return;
            }
            if (value === undefined || value === null) return;
            const normalized = normalizeSearchText(value);
            if (normalized) result.push(normalized);
        };
        visit(values);
        return result;
    }

    // A query is an AND of tokens, while each token may match any field.
    // This keeps a phrase such as "문서 연결" useful without requiring a
    // model or a server-specific full-text index in the browser.
    function matchesSearch(values, query) {
        const tokens = searchTokens(query);
        if (!tokens.length) return true;
        const haystack = searchableValues(values);
        return tokens.every(token => haystack.some(value => value.includes(token)));
    }

    return { normalizeSearchText, searchTokens, matchesSearch };
}));
