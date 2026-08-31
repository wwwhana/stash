(function () {
    'use strict';

    const root = document.documentElement;
    let preference = 'system';
    try {
        const stored = window.localStorage && window.localStorage.getItem('stash.theme');
        if (['system', 'light', 'dark'].includes(stored)) preference = stored;
    } catch (_) {
        // Blocked storage falls back to the operating system preference.
    }
    const resolved = preference === 'dark'
        || (preference === 'system' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches)
        ? 'dark'
        : 'light';
    root.dataset.stashThemePreference = preference;
    root.dataset.stashTheme = resolved;
    root.style.colorScheme = resolved;
}());
