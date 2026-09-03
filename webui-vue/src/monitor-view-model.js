import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { createMcpClient, pageItems } from './mcp-client.js';

const statusLabels = {
  backlog: '대기', ready: '준비', doing: '진행 중', blocked: '막힘', review: '검토',
  done: '완료', canceled: '취소', expired: '연결 만료'
};
const statusOrder = ['expired', 'blocked', 'doing', 'review', 'ready', 'backlog', 'done', 'canceled'];

const text = value => String(value ?? '').trim();
const number = value => Number.isFinite(Number(value)) ? Number(value) : 0;

function normalizeSlug(value) {
  const slug = text(value);
  if (!slug) return '/';
  return ('/' + slug.replace(/^\/+/, '')).replace(/\/+$/, '') || '/';
}

function readRoute(locationRef = window.location) {
  const params = new URLSearchParams(locationRef.search);
  return {
    project: text(params.get('project')), query: text(params.get('q')), status: text(params.get('status')),
    agent: text(params.get('agent')), focus: text(params.get('focus'))
  };
}

function syncRoute(state, historyRef = window.history, locationRef = window.location) {
  const params = new URLSearchParams();
  if (state.project) params.set('project', state.project);
  if (state.query) params.set('q', state.query);
  if (state.status) params.set('status', state.status);
  if (state.agent) params.set('agent', state.agent);
  if (state.focus) params.set('focus', state.focus);
  const next = `${locationRef.pathname}${params.toString() ? '?' + params.toString() : ''}`;
  historyRef.replaceState({}, '', next);
}

function resolveProjectNamespaces(items) {
  const seen = new Map();
  for (const item of items) {
    const slug = normalizeSlug(item?.slug);
    if (!/^\/projects\/[^/]+$/.test(slug) || seen.has(slug)) continue;
    const name = text(item?.name);
    seen.set(slug, { slug, label: name && name !== slug ? `${name} · ${slug}` : slug });
  }
  return [...seen.values()].sort((left, right) => left.slug.localeCompare(right.slug, 'ko'));
}

export function createMonitorViewModel(options = {}) {
  const windowRef = options.window || window;
  const fetchImpl = options.fetch || windowRef.fetch.bind(windowRef);
  const historyRef = options.history || windowRef.history;
  const route = readRoute(windowRef.location);
  const projects = ref([]);
  const project = ref(route.project);
  const query = ref(route.query);
  const status = ref(route.status);
  const agent = ref(route.agent);
  const focus = ref(route.focus);
  const map = ref({ goal_tree: { root_goal_id: null, goals: [] }, work_items: [], unassigned_work: [], edges: [] });
  const selected = ref(null);
  const loading = ref(false);
  const error = ref('');
  const auth = ref({ auth_mode: 'none', authenticated: false, user: '' });
  const onAuthenticationExpired = () => {
    auth.value = { ...auth.value, authenticated: false, user: '' };
  };
  const client = options.client || createMcpClient(fetchImpl, { onAuthenticationExpired });
  if (typeof client.setAuthenticationExpiredHandler === 'function') client.setAuthenticationExpiredHandler(onAuthenticationExpired);

  const allItems = computed(() => {
    const source = [...(map.value.work_items || []), ...(map.value.unassigned_work || [])];
    const seen = new Map();
    source.forEach(item => { if (number(item?.id)) seen.set(number(item.id), item); });
    return [...seen.values()];
  });
  const agents = computed(() => [...new Set(allItems.value.map(item => text(item.agent_id || item.owner)).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'ko')));
  const blockersByWork = computed(() => {
    const itemsByKey = new Map(allItems.value.map(item => [`work:${number(item.id)}`, item]));
    const blockers = new Map();
    for (const edge of map.value.edges || []) {
      if (edge?.relation !== 'blocks') continue;
      const blocker = itemsByKey.get(text(edge.from));
      const target = itemsByKey.get(text(edge.to));
      if (!blocker || !target || ['done', 'canceled'].includes(text(blocker.status))) continue;
      const key = number(target.id);
      blockers.set(key, [...(blockers.get(key) || []), blocker]);
    }
    return blockers;
  });
  const rows = computed(() => {
    const needle = query.value.toLocaleLowerCase('ko-KR');
    const rank = value => statusOrder.indexOf(value) === -1 ? statusOrder.length : statusOrder.indexOf(value);
    return allItems.value.filter(item => {
      const displayStatus = text(item.status);
      const searchable = [item.issue_key, item.title, item.description, item.owner, item.agent_id, item.latest_result, item.next_action, displayStatus].map(text).join(' ').toLocaleLowerCase('ko-KR');
      return (!needle || searchable.includes(needle)) && (!status.value || displayStatus === status.value) && (!agent.value || text(item.agent_id || item.owner) === agent.value);
    }).sort((left, right) => rank(text(left.status)) - rank(text(right.status)) || number(right.priority) - number(left.priority) || number(left.id) - number(right.id));
  });
  const rootGoal = computed(() => {
    const rootID = number(map.value.goal_tree?.root_goal_id);
    return (map.value.goal_tree?.goals || []).find(goal => number(goal.id) === rootID) || null;
  });
  const counts = computed(() => ({
    total: allItems.value.length, visible: rows.value.length,
    doing: allItems.value.filter(item => item.status === 'doing').length,
    blocked: allItems.value.filter(item => item.status === 'blocked').length
  }));
  const focused = computed(() => allItems.value.find(item => number(item.id) === number(focus.value)) || selected.value);

  function statusLabel(value) { return statusLabels[value] || value || '상태 없음'; }
  function progress(value) { return `${Math.round(Math.max(0, Math.min(1, number(value))) * 100)}%`; }
  function agentLabel(item) { return text(item?.agent_id || item?.owner) || '지정 안 됨'; }
  function blockers(item) { return blockersByWork.value.get(number(item?.id)) || []; }
  function blockerLabel(item) { return text(item?.issue_key || item?.title) || `#${number(item?.id)}`; }
  function routeState() { return { project: project.value, query: query.value, status: status.value, agent: agent.value, focus: focus.value }; }
  function select(item) { selected.value = item; focus.value = String(number(item?.id)); syncRoute(routeState(), historyRef, windowRef.location); }
  function clearSelection() { selected.value = null; focus.value = ''; syncRoute(routeState(), historyRef, windowRef.location); }
  function resetFilters() { query.value = ''; status.value = ''; agent.value = ''; syncRoute(routeState(), historyRef, windowRef.location); }
  async function load() {
    loading.value = true;
    error.value = '';
    selected.value = null;
    try {
      const statusResponse = await fetchImpl('/auth/status', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
      if (statusResponse.ok) auth.value = await statusResponse.json();
      const listed = [];
      let offset = 0;
      for (let page = 0; page < 100; page += 1) {
        const value = await client.call('list_namespaces', { limit: 101, offset });
        const items = pageItems(value);
        listed.push(...items.slice(0, 100));
        const hasMore = value?.has_more === undefined ? items.length > 100 : Boolean(value.has_more);
        if (!hasMore || !items.length) break;
        offset = number(value.next_offset) || offset + 100;
      }
      projects.value = resolveProjectNamespaces(listed);
      if (!projects.value.some(item => item.slug === project.value)) project.value = projects.value.length === 1 ? projects.value[0].slug : '';
      if (!project.value) { map.value = { goal_tree: { root_goal_id: null, goals: [] }, work_items: [], unassigned_work: [], edges: [] }; return; }
      map.value = await client.call('get_goal_map', { namespace: project.value, include_done: true }) || map.value;
      const match = allItems.value.find(item => number(item.id) === number(focus.value));
      selected.value = match || null;
      syncRoute({ ...routeState(), focus: match ? focus.value : '' }, historyRef, windowRef.location);
    } catch (cause) {
      error.value = text(cause?.message) || '작업 현황을 불러오지 못했습니다.';
    } finally {
      loading.value = false;
    }
  }
  function changeProject() { focus.value = ''; load(); }
  function handlePopState() { load(); }

  watch([query, status, agent], () => syncRoute(routeState(), historyRef, windowRef.location));
  onMounted(() => { load(); windowRef.addEventListener('popstate', handlePopState); });
  onUnmounted(() => windowRef.removeEventListener('popstate', handlePopState));

  return {
    projects, project, query, status, agent, map, selected, loading, error, auth,
    allItems, agents, rows, rootGoal, counts, focused, statusLabel, progress, agentLabel, blockers, blockerLabel,
    select, clearSelection, resetFilters, changeProject, load
  };
}
