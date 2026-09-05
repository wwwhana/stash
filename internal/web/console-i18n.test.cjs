const test = require('node:test');
const assert = require('node:assert/strict');
const { messages, detectLocale, translate: t, errorMessage } = require('./ui/console-i18n.js');

test('locale uses saved preference, supported browser languages, then Korean', () => {
    const window = { localStorage: { getItem: () => 'ko' }, navigator: { languages: ['en-US'] } };
    assert.equal(detectLocale(window), 'ko');
    window.localStorage.getItem = () => 'unsupported';
    assert.equal(detectLocale(window), 'en');
    window.localStorage.getItem = () => { throw new Error('storage denied'); };
    window.navigator.languages = ['fr-FR', 'ko-KR'];
    assert.equal(detectLocale(window), 'ko');
    window.navigator = { language: 'EN_gb' };
    assert.equal(detectLocale(window), 'en');
    assert.equal(detectLocale({}), 'ko');
});

test('both catalogs contain matching messages, placeholders, and guide API identifiers', () => {
    assert.deepEqual(Object.keys(messages.ko).sort(), Object.keys(messages.en).sort());
    const placeholders = value => [...new Set(value.match(/\{\w+\}/g))].sort();
    for (const key of Object.keys(messages.ko)) {
        const english = messages.en[key];
        for (const value of typeof english === 'string' ? [english] : Object.values(english)) {
            assert.ok(value.length > 0, key);
            assert.deepEqual(placeholders(messages.ko[key]), placeholders(value), key);
        }
    }
    const identifiers = guide => [...new Set(guide.match(/`[a-z][a-z0-9_.-]*`/g))].sort();
    // The Korean guide also quotes API values named as prose in the English guide.
    for (const identifier of identifiers(messages.en['agent.guide'])) assert.ok(messages.ko['agent.guide'].includes(identifier), identifier);
});

test('counts, plural forms, and stored errors render in the selected language', () => {
    assert.equal(t('en', 'view.shownCount', { count: 1 }), '1 item shown');
    assert.equal(t('en', 'view.shownCount', { count: 1200 }), '1,200 items shown');
    assert.equal(t('ko', 'view.shownCount', { count: 1200 }), '1,200개 표시');
    const error = errorMessage({ status: 401 });
    assert.match(t('en', error), /session expired/);
    assert.match(t('ko', error), /로그인이 만료/);
    assert.equal(t('en', errorMessage(new Error('기억이 변경되었습니다. 다시 여세요.'))), 'This memory changed. Please reopen it.');
    assert.equal(t('en', errorMessage(new Error('internal untranslated diagnostic'))), 'Could not process the request.');
});
