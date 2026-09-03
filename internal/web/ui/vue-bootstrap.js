(function (root) {
    'use strict';

    root.process = root.process || { env: { NODE_ENV: 'production' } };

    const applyTheme = () => {
        let preference = 'system';
        try {
            const stored = root.localStorage.getItem('stash.theme');
            if (['system', 'light', 'dark'].includes(stored)) preference = stored;
        } catch (_) {
            // 시스템 설정을 사용한다.
        }
        const media = root.matchMedia && root.matchMedia('(prefers-color-scheme: dark)');
        const dark = preference === 'dark' || (preference === 'system' && media && media.matches);
        document.documentElement.dataset.stashThemePreference = preference;
        document.documentElement.dataset.stashTheme = dark ? 'dark' : 'light';
        document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
    };

    applyTheme();
    const media = root.matchMedia && root.matchMedia('(prefers-color-scheme: dark)');
    if (media && media.addEventListener) media.addEventListener('change', applyTheme);
    root.addEventListener('storage', event => {
        if (event.key === 'stash.theme') applyTheme();
    });
    root.stashConsoleApplyTheme = applyTheme;
    root.stashVueApplyTheme = applyTheme;
}(typeof globalThis !== 'undefined' ? globalThis : window));
