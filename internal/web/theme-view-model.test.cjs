const test = require('node:test');
const assert = require('node:assert/strict');

const { createThemeViewModel, normalizeThemePreference, resolveTheme } = require('./ui/theme-view-model.js');

function harness(matches = false) {
    const listeners = new Set();
    const media = {
        matches,
        addEventListener(_type, listener) { listeners.add(listener); },
        removeEventListener(_type, listener) { listeners.delete(listener); }
    };
    const values = new Map();
    const root = { dataset: {}, style: {} };
    const windowRef = {
        localStorage: {
            getItem(key) { return values.get(key) || null; },
            setItem(key, value) { values.set(key, value); }
        },
        matchMedia() { return media; }
    };
    return { media, listeners, values, root, windowRef, documentRef: { documentElement: root } };
}

test('theme preference defaults to system and resolves from the operating system', () => {
    const value = harness(true);
    const vm = createThemeViewModel({ window: value.windowRef, document: value.documentRef, storage: value.windowRef.localStorage });

    assert.equal(vm.themePreference, 'system');
    assert.equal(resolveTheme('system', value.windowRef), 'dark');
    assert.equal(normalizeThemePreference('unknown'), 'system');
    assert.equal(vm.initTheme(), 'dark');
    assert.equal(value.root.dataset.stashThemePreference, 'system');
    assert.equal(value.root.dataset.stashTheme, 'dark');
    assert.equal(value.root.style.colorScheme, 'dark');
    assert.equal(value.listeners.size, 1);
});

test('explicit choices persist and system changes follow only the system choice', () => {
    const value = harness(false);
    const vm = createThemeViewModel({ window: value.windowRef, document: value.documentRef, storage: value.windowRef.localStorage });
    vm.initTheme();

    assert.equal(vm.setThemePreference('dark'), 'dark');
    assert.equal(value.values.get('stash.theme'), 'dark');
    assert.equal(value.listeners.size, 0);
    value.media.matches = true;
    value.listeners.forEach(listener => listener({ matches: true }));
    assert.equal(vm.themeResolved, 'dark');

    vm.setThemePreference('system');
    assert.equal(value.listeners.size, 1);
    value.media.matches = true;
    value.listeners.forEach(listener => listener({ matches: true }));
    assert.equal(vm.themeResolved, 'dark');
    value.media.matches = false;
    value.listeners.forEach(listener => listener({ matches: false }));
    assert.equal(vm.themeResolved, 'light');
});
