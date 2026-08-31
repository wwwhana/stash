const test = require('node:test');
const assert = require('node:assert/strict');

const { normalizeSearchText, searchTokens, matchesSearch } = require('./ui/search-utils.js');

test('search normalization is Unicode-safe and removes duplicate whitespace tokens', () => {
    assert.equal(normalizeSearchText('  Ａ-１  문서  '), 'a-1  문서');
    assert.deepEqual(searchTokens('문서 연결 문서'), ['문서', '연결']);
});

test('each search token may match a different field but all tokens are required', () => {
    assert.equal(matchesSearch(['W-1', '문서 연결', ['codex']], '문서 codex'), true);
    assert.equal(matchesSearch(['W-1', '문서 연결', ['codex']], '문서 jira'), false);
    assert.equal(matchesSearch(['W-1', '문서 연결'], ''), true);
});
