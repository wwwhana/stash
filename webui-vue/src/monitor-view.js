import { createApp } from 'vue';
import { createMonitorViewModel } from './monitor-view-model.js';

const template = `
  <div class="vue-monitor-shell">
    <header class="vue-monitor-header">
      <div>
        <p class="vue-monitor-kicker">STASH / MONITOR</p>
        <h1>작업 관제</h1>
        <p class="vue-monitor-subtitle">프로젝트의 작업과 에이전트 상태를 한 화면에서 확인합니다.</p>
      </div>
      <div class="vue-monitor-header-actions">
        <a class="vue-monitor-login" href="/">전체 화면</a>
        <span v-if="auth.authenticated" class="vue-monitor-auth">{{ auth.user || '로그인됨' }}</span>
        <a v-else-if="auth.auth_mode !== 'none'" class="vue-monitor-login" href="/auth/login">로그인</a>
        <button type="button" class="vue-monitor-refresh" @click="load" :disabled="loading">{{ loading ? '불러오는 중' : '새로 고침' }}</button>
      </div>
    </header>
    <section class="vue-monitor-toolbar" aria-label="작업 필터">
      <label class="vue-monitor-field vue-monitor-project"><span>프로젝트</span><select v-model="project" @change="changeProject"><option value="">선택하세요</option><option v-for="item in projects" :key="item.slug" :value="item.slug">{{ item.label }}</option></select></label>
      <label class="vue-monitor-field vue-monitor-search"><span>검색</span><input v-model="query" type="search" placeholder="작업명, 에이전트, 결과"></label>
      <label class="vue-monitor-field"><span>상태</span><select v-model="status"><option value="">전체</option><option value="backlog">대기</option><option value="ready">준비</option><option value="doing">진행 중</option><option value="blocked">막힘</option><option value="review">검토</option><option value="done">완료</option><option value="canceled">취소</option><option value="expired">연결 만료</option></select></label>
      <label class="vue-monitor-field"><span>에이전트</span><select v-model="agent"><option value="">전체</option><option v-for="item in agents" :key="item" :value="item">{{ item }}</option></select></label>
      <button v-if="query || status || agent" type="button" class="vue-monitor-clear" @click="resetFilters">필터 지우기</button>
    </section>
    <div v-if="error" class="vue-monitor-alert" role="alert">{{ error }} <a v-if="auth.auth_mode !== 'none' && !auth.authenticated" href="/auth/login">로그인</a></div>
    <section v-if="rootGoal" class="vue-monitor-goal" aria-label="공통 목표"><span>공통 목표</span><strong>{{ rootGoal.content }}</strong><em>{{ progress(rootGoal.progress) }}</em></section>
    <section class="vue-monitor-summary" aria-label="작업 요약"><div><strong>{{ counts.visible }}</strong><span>표시 작업</span></div><div><strong>{{ counts.total }}</strong><span>전체 작업</span></div><div><strong>{{ counts.doing }}</strong><span>진행 중</span></div><div class="is-danger"><strong>{{ counts.blocked }}</strong><span>막힘</span></div></section>
    <main class="vue-monitor-content">
      <section class="vue-monitor-table-card" aria-label="작업 목록">
        <div v-if="loading" class="vue-monitor-empty">작업 현황을 불러오는 중입니다.</div>
        <div v-else-if="!project" class="vue-monitor-empty"><strong>프로젝트를 선택하세요.</strong><span>프로젝트를 선택하면 작업 흐름이 표시됩니다.</span></div>
        <div v-else-if="!rows.length" class="vue-monitor-empty"><strong>표시할 작업이 없습니다.</strong><span>검색어나 필터를 바꿔 보세요.</span></div>
        <div v-else class="vue-monitor-table-wrap"><table><thead><tr><th>작업</th><th>상태</th><th>에이전트</th><th>막힘</th><th>다음 할 일</th></tr></thead><tbody><tr v-for="item in rows" :key="item.id" :class="{ 'is-selected': selected && selected.id === item.id }" @click="select(item)"><td><button type="button" class="vue-monitor-item-button"><b>{{ item.issue_key || '#' + item.id }}</b><strong>{{ item.title }}</strong><small>{{ item.latest_result || item.description || '설명 없음' }}</small></button></td><td><span class="vue-monitor-status" :data-status="item.status">{{ statusLabel(item.status) }}</span></td><td>{{ agentLabel(item) }}</td><td><span v-if="item.status === 'blocked'" class="vue-monitor-blocker">상태: 막힘</span><span v-for="blocker in blockers(item)" :key="blocker.id" class="vue-monitor-blocker">{{ blockerLabel(blocker) }}</span><span v-if="item.status !== 'blocked' && !blockers(item).length">없음</span></td><td>{{ item.next_action || '다음 할 일 없음' }}</td></tr></tbody></table></div>
      </section>
      <aside v-if="focused" class="vue-monitor-detail" aria-label="선택한 작업"><div class="vue-monitor-detail-head"><div><span>{{ focused.issue_key || '#' + focused.id }}</span><h2>{{ focused.title }}</h2></div><button type="button" aria-label="선택 해제" @click="clearSelection">×</button></div><dl><div><dt>상태</dt><dd>{{ statusLabel(focused.status) }}</dd></div><div><dt>에이전트</dt><dd>{{ agentLabel(focused) }}</dd></div><div><dt>막는 작업</dt><dd><span v-if="focused.status === 'blocked'">상태: 막힘</span><span v-for="blocker in blockers(focused)" :key="blocker.id">{{ blockerLabel(blocker) }}</span><span v-if="focused.status !== 'blocked' && !blockers(focused).length">없음</span></dd></div><div><dt>결과</dt><dd>{{ focused.latest_result || '기록 없음' }}</dd></div><div><dt>다음 할 일</dt><dd>{{ focused.next_action || '기록 없음' }}</dd></div></dl><a class="vue-monitor-open" :href="'/ui/issues?project=' + encodeURIComponent(project) + '&issue=' + focused.id">작업 상세 열기</a></aside>
    </main>
  </div>
`;

function applyTheme() {
  const root = document.documentElement;
  let preference = 'system';
  try { preference = ['system', 'light', 'dark'].includes(localStorage.getItem('stash.theme')) ? localStorage.getItem('stash.theme') : 'system'; } catch (_) { /* use system */ }
  const dark = preference === 'dark' || (preference === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches);
  root.dataset.stashThemePreference = preference;
  root.dataset.stashTheme = dark ? 'dark' : 'light';
  root.style.colorScheme = dark ? 'dark' : 'light';
}

applyTheme();
const mount = document.querySelector('[data-stash-vue-monitor]');
if (mount) createApp({ template, setup: createMonitorViewModel }).mount(mount);
