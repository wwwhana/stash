(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashThemeViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const storageKey = 'stash.theme';
    const preferences = ['system', 'light', 'dark'];
    const labels = { system: '시스템', light: '밝게', dark: '어둡게' };

    function normalizeThemePreference(value) {
        const candidate = String(value || '').trim().toLowerCase();
        return preferences.includes(candidate) ? candidate : 'system';
    }

    function systemTheme(windowRef) {
        try {
            return windowRef && typeof windowRef.matchMedia === 'function'
                && windowRef.matchMedia('(prefers-color-scheme: dark)').matches
                ? 'dark'
                : 'light';
        } catch (_) {
            return 'light';
        }
    }

    function resolveTheme(preference, windowRef) {
        const normalized = normalizeThemePreference(preference);
        return normalized === 'system' ? systemTheme(windowRef) : normalized;
    }

    function browserStorage(windowRef) {
        try {
            return windowRef && windowRef.localStorage ? windowRef.localStorage : null;
        } catch (_) {
            return null;
        }
    }

    function readPreference(storage) {
        try {
            return normalizeThemePreference(storage && storage.getItem(storageKey));
        } catch (_) {
            return 'system';
        }
    }

    function writePreference(storage, preference) {
        try {
            if (storage && typeof storage.setItem === 'function') storage.setItem(storageKey, preference);
        } catch (_) {
            // Private browsing and blocked storage must not prevent the UI from changing theme.
        }
    }

    function createThemeViewModel(options = {}) {
        const windowRef = options.window || (typeof window !== 'undefined' ? window : null);
        const documentRef = options.document || (typeof document !== 'undefined' ? document : null);
        const storage = Object.prototype.hasOwnProperty.call(options, 'storage')
            ? options.storage
            : browserStorage(windowRef);
        let mediaQuery = null;
        let mediaListener = null;

        const viewModel = {
            themePreference: readPreference(storage),
            themeResolved: resolveTheme(readPreference(storage), windowRef),
            themeOptions: preferences.map(value => ({ value, label: labels[value] })),
            themeInitialized: false,

            themePreferenceLabel(value = this.themePreference) {
                return labels[normalizeThemePreference(value)];
            },

            themeResolvedLabel() {
                return labels[this.themeResolved] || labels.light;
            },

            applyThemePreference(value = this.themePreference) {
                const preference = normalizeThemePreference(value);
                const resolved = resolveTheme(preference, windowRef);
                this.themeResolved = resolved;
                if (documentRef && documentRef.documentElement) {
                    const element = documentRef.documentElement;
                    if (element.dataset) {
                        element.dataset.stashThemePreference = preference;
                        element.dataset.stashTheme = resolved;
                    } else {
                        element.setAttribute('data-stash-theme-preference', preference);
                        element.setAttribute('data-stash-theme', resolved);
                    }
                    if (element.style) element.style.colorScheme = resolved;
                }
                return resolved;
            },

            detachThemeSystemListener() {
                if (!mediaQuery || !mediaListener) return;
                try {
                    if (typeof mediaQuery.removeEventListener === 'function') mediaQuery.removeEventListener('change', mediaListener);
                    else if (typeof mediaQuery.removeListener === 'function') mediaQuery.removeListener(mediaListener);
                } catch (_) {
                    // A browser may revoke the media query object during navigation.
                }
                mediaQuery = null;
                mediaListener = null;
            },

            syncThemeSystemListener() {
                this.detachThemeSystemListener();
                if (this.themePreference !== 'system' || !windowRef || typeof windowRef.matchMedia !== 'function') return;
                try {
                    mediaQuery = windowRef.matchMedia('(prefers-color-scheme: dark)');
                    mediaListener = () => {
                        if (this.themePreference === 'system') this.applyThemePreference('system');
                    };
                    if (typeof mediaQuery.addEventListener === 'function') mediaQuery.addEventListener('change', mediaListener);
                    else if (typeof mediaQuery.addListener === 'function') mediaQuery.addListener(mediaListener);
                } catch (_) {
                    mediaQuery = null;
                    mediaListener = null;
                }
            },

            initTheme() {
                if (this.themeInitialized) return this.themeResolved;
                this.themeInitialized = true;
                this.themePreference = normalizeThemePreference(this.themePreference);
                this.applyThemePreference(this.themePreference);
                this.syncThemeSystemListener();
                return this.themeResolved;
            },

            setThemePreference(value) {
                this.themePreference = normalizeThemePreference(value);
                writePreference(storage, this.themePreference);
                this.applyThemePreference(this.themePreference);
                this.syncThemeSystemListener();
                return this.themeResolved;
            }
        };

        return viewModel;
    }

    return {
        createThemeViewModel,
        normalizeThemePreference,
        resolveTheme,
        storageKey
    };
}));
