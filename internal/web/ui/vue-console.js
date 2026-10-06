(function (root) {
    'use strict';
    const template = `
<section v-if="authLoading || !authChecked || needsLogin" class="stash-login" :aria-label="t('auth.login')">
  <div class="stash-login-card"><div class="stash-brand"><span class="stash-brand-mark">S</span><span>Stash</span></div>
    <div class="stash-login-language"><select class="stash-language-select" :aria-label="t('language.label')" :value="locale" @change="changeLocale($event.target.value)"><option value="ko">한국어</option><option value="en">English</option></select></div>
    <p v-if="authLoading" role="status">{{ t('auth.checking') }}</p>
    <template v-else-if="!authChecked"><p class="stash-error" role="alert">{{ t(error) }}</p><button type="button" class="stash-button" @click="bootstrap">{{ t('action.retry') }}</button></template>
    <template v-else><h1>{{ t('auth.heading') }}</h1>
      <form v-if="canLocalLogin" class="stash-login-form" @submit.prevent="submitLogin">
        <label class="stash-field"><span>{{ t('auth.username') }}</span><input v-model="loginForm.username" name="username" autocomplete="username" autocapitalize="off" spellcheck="false" required autofocus :disabled="loginBusy"></label>
        <label class="stash-field"><span>{{ t('auth.password') }}</span><input v-model="loginForm.password" name="password" type="password" autocomplete="current-password" required :disabled="loginBusy"></label>
        <p v-if="loginError" class="stash-error" role="alert">{{ t(loginError) }}</p>
        <button type="submit" class="stash-button is-primary" :disabled="loginBusy">{{ loginBusy ? t('auth.loggingIn') : t('auth.login') }}</button>
      </form>
      <a v-if="canSSOLogin" class="stash-button" :class="{'is-primary': !canLocalLogin}" href="/auth/login?provider=oidc" @click.prevent="beginLogin('oidc')">{{ t('auth.sso') }}</a>
      <a v-if="!canLocalLogin && !canSSOLogin" class="stash-button is-primary" href="/auth/login" @click.prevent="beginLogin()">{{ t('auth.login') }}</a>
      <a v-else class="stash-login-alt" href="/auth/login?provider=token" @click.prevent="beginLogin('token')">{{ t('auth.withToken') }}</a>
    </template>
  </div>
</section>
<div v-else class="stash-console" @keydown.esc="route.detail ? closeDetail() : clearSelection()">
  <aside class="stash-sidebar" :aria-label="t('nav.main')">
    <div class="stash-brand"><span class="stash-brand-mark">S</span><span>Stash</span></div>
    <label class="stash-root-select"><span>{{ t('nav.workspaces') }}</span><select v-model="rootSlug" :title="rootSlug" @change="changeRoot"><option v-for="item in rootOptions" :key="item.slug" :value="item.slug">{{ item.name || (item.slug === '/' ? t('workspace.default') : item.slug) }}</option></select></label>
    <select class="stash-mobile-nav" :aria-label="t('nav.page')" :value="route.route === 'wiki_page' ? 'wiki' : route.route" @change="navigate($event.target.value)"><optgroup :label="t('nav.sectionWiki')"><option value="wiki">{{ t('nav.wikiHome') }}</option></optgroup><optgroup :label="t('nav.sectionMemory')"><option value="list_memories">{{ t('nav.memories') }}</option><option value="list_goals">{{ t('nav.goals') }}</option><option v-for="item in navItems" :key="item.route" :value="item.route">{{ item.label }}</option></optgroup><optgroup :label="t('nav.sectionServer')"><option v-if="showAdminNav" value="llm">{{ t('nav.llm') }}</option><option v-if="showAdminNav" value="maintenance">{{ t('nav.maintenance') }}</option><option value="list_namespaces">{{ t('nav.manageWorkspaces') }}</option><option value="tokens">{{ t('nav.tokens') }}</option><option value="agent">{{ t('nav.agent') }}</option></optgroup></select>
    <nav class="stash-nav">
      <span class="stash-nav-label">{{ t('nav.sectionWiki') }}</span>
      <a :href="navHref('wiki')" :class="{'is-active': ['wiki', 'wiki_page'].includes(route.route)}" :aria-current="['wiki', 'wiki_page'].includes(route.route) ? 'page' : null" @click.prevent="navigate('wiki')"><span class="stash-nav-icon">▤</span><span>{{ t('nav.wikiHome') }}</span></a>
      <span class="stash-nav-label">{{ t('nav.sectionMemory') }}</span>
      <a :href="navHref('list_memories')" :class="{'is-active': ['list_memories', 'query_facts', 'list_hypotheses'].includes(route.route)}" @click.prevent="navigate('list_memories')"><span class="stash-nav-icon">✓</span><span>{{ t('nav.memories') }}</span></a>
      <a :href="navHref('list_goals')" :class="{'is-active': route.route === 'list_goals'}" @click.prevent="navigate('list_goals')"><span class="stash-nav-icon">↗</span><span>{{ t('nav.goals') }}</span></a>
      <a v-for="item in navItems" :key="item.route" :href="navHref(item.route)" :class="{'is-active': route.route === item.route}" :aria-current="route.route === item.route ? 'page' : null" @click.prevent="navigate(item.route)"><span class="stash-nav-icon">{{ item.icon }}</span><span>{{ item.label }}</span></a>
      <span class="stash-nav-label">{{ t('nav.sectionServer') }}</span>
      <a v-if="showAdminNav" :href="navHref('llm')" :class="{'is-active': route.route === 'llm'}" @click.prevent="navigate('llm')"><span class="stash-nav-icon">⚙</span><span>{{ t('nav.llm') }}</span></a>
      <a v-if="showAdminNav" :href="navHref('maintenance')" :class="{'is-active': route.route === 'maintenance'}" @click.prevent="navigate('maintenance')"><span class="stash-nav-icon">↻</span><span>{{ t('nav.maintenance') }}</span></a>
      <a :href="navHref('list_namespaces')" :class="{'is-active': route.route === 'list_namespaces'}" @click.prevent="navigate('list_namespaces')"><span class="stash-nav-icon">◌</span><span>{{ t('nav.manageWorkspaces') }}</span></a>
      <a :href="navHref('tokens')" :class="{'is-active': route.route === 'tokens'}" @click.prevent="navigate('tokens')"><span class="stash-nav-icon">⚿</span><span>{{ t('nav.tokens') }}</span></a>
      <a :href="navHref('agent')" :class="{'is-active': route.route === 'agent'}" @click.prevent="navigate('agent')"><span class="stash-nav-icon">☰</span><span>{{ t('nav.agent') }}</span></a>
    </nav>
    <div class="stash-sidebar-settings">
      <label><span>{{ t('theme.label') }}</span><select class="stash-theme-select" :value="themePreference" @change="changeTheme($event.target.value)"><option value="system">{{ t('theme.system') }}</option><option value="light">{{ t('theme.light') }}</option><option value="dark">{{ t('theme.dark') }}</option></select></label>
      <label><span>{{ t('language.label') }}</span><select class="stash-language-select" :value="locale" @change="changeLocale($event.target.value)"><option value="ko">한국어</option><option value="en">English</option></select></label>
    </div>
  </aside>

  <main class="stash-main">
    <header class="stash-topbar">
      <div><h1>{{ pageTitle }}</h1></div>
      <div class="stash-top-actions">

        <a v-if="canLogin && !auth.authenticated" class="stash-button is-primary" href="/auth/login" @click.prevent="beginLogin">{{ t('auth.login') }}</a>
        <button v-else-if="auth.authenticated" type="button" class="stash-button" :aria-expanded="authPanelOpen" aria-controls="stash-account" @click="authPanelOpen = !authPanelOpen">{{ t('auth.account') }}</button>
        <button type="button" class="stash-button" :disabled="loading || refreshing" @click="loadRoute()" :aria-label="t('action.refresh')" :title="lastRefreshLabel || t('action.refresh')">↻</button>
      </div>
    </header>

    <section v-if="authPanelOpen" id="stash-account" class="stash-token-panel" @keydown.esc="authPanelOpen = false">
      <div class="stash-inspector-head"><h3>{{ t('auth.account') }}</h3><button type="button" :aria-label="t('auth.closeAccount')" @click="authPanelOpen = false">×</button></div>
      <p class="stash-account-user"><strong>{{ auth.user }}</strong><span v-if="auth.admin"> · {{ t('auth.administrator') }}</span></p>
      <form v-if="auth.has_password" class="stash-password-form" @submit.prevent="changePassword">
        <strong>{{ t('auth.changePassword') }}</strong>
        <label class="stash-field"><span>{{ t('auth.currentPassword') }}</span><input v-model="passwordForm.current" type="password" autocomplete="current-password" :disabled="passwordBusy"></label>
        <label class="stash-field"><span>{{ t('auth.newPassword') }}</span><input v-model="passwordForm.next" type="password" autocomplete="new-password" minlength="8" maxlength="72" :disabled="passwordBusy"></label>
        <label class="stash-field"><span>{{ t('auth.confirmPassword') }}</span><input v-model="passwordForm.confirm" type="password" autocomplete="new-password" :disabled="passwordBusy"></label>
        <p v-if="passwordError" class="stash-error" role="alert">{{ t(passwordError) }}</p>
        <p v-if="passwordNotice" class="stash-context-note" role="status">{{ t(passwordNotice) }}</p>
        <button type="submit" class="stash-button" :disabled="passwordBusy">{{ passwordBusy ? t('auth.saving') : t('auth.changePassword') }}</button>
      </form>
      <div class="stash-token-actions"><button type="button" class="stash-button" @click="navigate('tokens'); authPanelOpen = false">{{ t('auth.issueToken') }}</button><a class="stash-button" :href="navHref('tokens')" @click.prevent="navigate('tokens'); authPanelOpen = false">{{ t('auth.manageTokens') }}</a><a class="stash-button is-quiet" href="/auth/logout" @click.prevent="logout">{{ t('auth.logout') }}</a></div>
      <div class="stash-token-issued" v-if="issuedToken"><code>{{ issuedToken }}</code><button type="button" class="stash-button" @click="copyIssuedToken">{{ t('auth.copyToken') }}</button></div>
      <div v-if="tokenError" class="stash-error">{{ t(tokenError) }}</div>
    </section>

    <section class="stash-surface">
      <div v-if="error" class="stash-error" role="alert">{{ t(error) }} <button type="button" class="stash-button" @click="loadRoute()">{{ t('action.retry') }}</button> <button v-if="['goal-map', 'monitor'].includes(route.route)" type="button" class="stash-button" @click="navigate('board')">{{ t('view.workList') }}</button></div>
      <div v-if="refreshError" class="stash-context-note" role="status">{{ t(refreshError) }} <button type="button" class="stash-button" :disabled="refreshing" @click="refreshVisible">{{ t('action.retry') }}</button></div>
      <div v-if="loading" class="stash-loading" role="status">{{ t('view.loading') }}</div>
      <div v-else-if="!error" class="stash-content-grid" :class="{'has-inspector': selected && !route.detail}">
        <div v-if="contextError" class="stash-context-note" role="status">{{ t(contextError) }} <button v-if="selected" type="button" class="stash-button" @click="loadSelectionContext">{{ t('action.retry') }}</button></div>
        <div v-if="!route.detail && (map.resources_truncated || graph.has_more)" class="stash-context-note">{{ t('view.partial') }}</div>
        <section>
          <article v-if="route.detail && selected" class="stash-detail" tabindex="-1">
            <header><div><p class="stash-kicker">{{ t('view.detailTitle', { kind: kindLabel(selected.kind) }) }}</p><h2>{{ selectedTitle }}</h2></div><button type="button" class="stash-button" @click="closeDetail">{{ t('action.backToList') }}</button></header>
            <p v-if="selectionLoading" role="status">{{ t('view.loadingDetail') }}</p><p v-if="detailLoading" role="status">{{ t('view.loadingOriginal') }}</p><p v-if="detailError" class="stash-error" role="alert">{{ t(detailError) }} <button type="button" class="stash-button" @click="loadMemoryDetail">{{ t('action.retry') }}</button></p><dl><div v-for="field in selectedFields" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div></dl>
            <div v-if="selectedParent || selectedChildren.length" class="stash-related-links"><div v-if="selectedParent"><span>{{ t('view.parentTitle', { kind: kindLabel(selectedParent.kind) }) }}</span><button type="button" @click="selectObject(selectedParent.kind, selectedParent.item)">{{ itemTitle(selectedParent.kind, selectedParent.item) }}</button></div><div v-if="selectedChildren.length"><span>{{ t('view.childCount', { count: selectedChildren.length }) }}</span><div><button v-for="child in selectedChildren" :key="child.key" type="button" @click="selectObject(child.kind, child.item)">{{ itemTitle(child.kind, child.item) }}</button></div></div></div>
            <div v-if="selectedConnections.length" class="stash-related-links" :aria-label="t('view.connections')"><div v-for="connection in selectedConnections" :key="connection.key"><span>{{ connection.label }}</span><button type="button" @click="selectObject(connection.kind, connection.item)">{{ itemTitle(connection.kind, connection.item) }}</button></div></div>
          </article>

          <template v-else-if="route.route === 'goal-map'">
            <section v-if="attentionItems.length" class="stash-attention" :aria-label="t('attention.title')"><header><h2>{{ t('attention.title') }} <span>{{ formatNumber(attentionItems.length) }}</span></h2><button v-if="attentionItems.length > 3" type="button" class="stash-button is-quiet" :aria-expanded="attentionExpanded" @click="attentionExpanded = !attentionExpanded">{{ attentionExpanded ? t('action.less') : t('action.more') }}</button></header><ul><li v-for="entry in (attentionExpanded ? attentionItems : attentionItems.slice(0, 3))" :key="entry.key"><button type="button" @click="selectObject(entry.target.kind, entry.target.item)"><strong>{{ itemTitle(entry.target.kind, entry.target.item) }}</strong><span>{{ t('attention.' + entry.reason) }}</span></button></li></ul></section>
            <div class="stash-overview-tools">
              <label class="stash-field is-search"><span class="stash-sr-only">{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="t('search.overview')" @input="syncURL"></label>
              <details class="stash-filters"><summary>{{ t('filter.label') }}<span v-if="hasFilters" class="stash-filter-dot"></span></summary><div class="stash-filter-panel">
                <label class="stash-field"><span>{{ t('field.workStatus') }}</span><select v-model="filters.status" @change="syncURL"><option value="">{{ t('filter.all') }}</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label>
                <label class="stash-field"><span>{{ t('field.owner') }}</span><select v-model="filters.agent" @change="syncURL"><option value="">{{ t('filter.all') }}</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label>
                <label class="stash-field"><span>{{ t('field.memoryType') }}</span><select v-model="filters.memoryType" @change="syncURL"><option value="">{{ t('filter.all') }}</option><option value="fact">{{ t('nav.facts') }}</option><option value="episode">{{ t('memory.episode') }}</option><option value="hypothesis">{{ t('nav.hypotheses') }}</option><option value="failure">{{ t('memory.failure') }}</option></select></label>
                <button type="button" class="stash-button" @click="resetFilters">{{ t('action.reset') }}</button>
              </div></details>
              <div class="stash-view-switch" :aria-label="t('view.mode')"><button type="button" :aria-pressed="mapAsList" @click="mapAsList = true">{{ t('view.list') }}</button><button type="button" :aria-pressed="!mapAsList" @click="mapAsList = false">{{ t('view.map') }}</button></div>
            </div>
            <div class="stash-kind-tabs" :aria-label="t('view.kinds')"><button type="button" :aria-pressed="activeKind === 'all'" @click="showKind('all')">{{ t('filter.all') }} <span>{{ formatNumber(rootCounts.goal + rootCounts.work + rootCounts.memory + rootCounts.resource) }}</span></button><button v-for="kind in kindOrder" :key="kind" type="button" :aria-pressed="activeKind === kind" @click="showKind(kind)">{{ kind === 'memory' ? t('view.linkedMemory') : kindNames[kind] }} <span>{{ formatNumber(rootCounts[kind]) }}</span></button></div>
            <div v-if="!mapAsList" class="stash-legend"><button type="button" class="stash-button" @click="fitMap = !fitMap">{{ fitMap ? t('view.actualSize') : t('view.fit') }}</button><span><i></i>{{ t('map.goalLink') }}</span><span><i class="is-dash"></i>{{ t('map.context') }}</span></div>
            <div v-if="!mapLayout.nodes.length" class="stash-empty"><strong>{{ hasFilters ? t('empty.filtered') : t('empty.overview') }}</strong><button v-if="hasFilters" type="button" class="stash-button" @click="resetFilters">{{ t('action.clearFilters') }}</button></div>
            <div v-else-if="mapAsList" class="stash-list"><button v-for="node in mapLayout.nodes" :key="node.key" type="button" class="stash-list-item" :class="{'is-selected': selected && selected.key === node.key}" @click="selectMapNode(node)"><span><strong>{{ nodeTitle(node) }}</strong><small v-if="node.kind === 'goal'">{{ goalProgressLabel(node.item) }}</small><small v-else-if="node.kind === 'work'">{{ workNote(node.item) }}</small></span><span class="stash-list-meta"><span>{{ kindLabel(node.kind) }}</span><span v-if="node.kind === 'work'" class="stash-status" :data-status="displayStatus(node.item)">{{ statusLabel(displayStatus(node.item)) }}</span><span v-else-if="node.kind === 'memory'">{{ memoryTypeLabel(node.item.memory_type) }}</span></span></button></div>
            <div v-else class="stash-map-viewport"><div class="stash-map-canvas" :style="canvasStyle(mapLayout)"><svg class="stash-map-edge-layer" :viewBox="'0 0 ' + mapLayout.width + ' ' + mapLayout.height" aria-hidden="true"><defs><marker id="stash-map-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#818cf8"></path></marker></defs><path v-for="edge in mapLayout.edges" :key="edge.key" :d="edge.path" :stroke="edge.stroke" stroke-width="2" :stroke-dasharray="edge.dashArray || null" :marker-end="edge.marker ? 'url(#stash-map-arrow)' : null" fill="none"></path></svg><div v-for="ring in mapLayout.rings" :key="ring.key" class="stash-map-ring" :style="ring.style"><span>{{ t('map.ring.' + ring.key) }} · {{ formatNumber(ring.count) }}</span></div><button v-for="node in mapLayout.nodes" :key="node.key" type="button" class="stash-map-node" :class="mapNodeClasses(node)" :style="node.style" :aria-label="nodeAria(node)" @click="selectMapNode(node)"><span class="stash-node-meta"><span class="stash-node-key">{{ nodeKey(node) }}</span><span v-if="node.kind === 'work'" class="stash-status" :data-status="displayStatus(node.item)">{{ statusLabel(displayStatus(node.item)) }}</span><span v-else>{{ kindLabel(node.kind) }}</span></span><span class="stash-node-title">{{ nodeTitle(node) }}</span><span v-if="node.kind === 'goal'" class="stash-node-note">{{ goalProgressLabel(node.item) }}</span><span v-if="node.kind === 'work'" class="stash-node-note">{{ workNote(node.item) }}</span><span v-else-if="node.kind === 'resource'" class="stash-node-note">{{ node.item.source || t('resource.linked') }}</span><span v-else-if="node.kind === 'memory'" class="stash-node-note">{{ memoryTypeLabel(node.item.memory_type) }}</span></button></div></div>
          </template>

          <template v-else-if="route.route === 'wiki'">
            <div class="stash-wiki-home">
              <p class="stash-wiki-intro">{{ t('wiki.description') }}</p>
              <div class="stash-toolbar stash-wiki-toolbar">
                <label class="stash-field is-search"><span class="stash-sr-only">{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="t('wiki.search')" @input="syncURL" @keydown.enter="searchList"></label>
                <label class="stash-field"><span>{{ t('wiki.kind') }}</span><select v-model="wikiFilters.kind" @change="searchWiki"><option value="">{{ t('wiki.allKinds') }}</option><option v-for="kind in wikiKinds" :key="kind" :value="kind">{{ t('wiki.kind.' + kind) }}</option></select></label>
                <label class="stash-check"><input v-model="wikiFilters.stale" type="checkbox" @change="searchWiki"><span>{{ t('wiki.staleOnly') }}</span></label>
                <button type="button" class="stash-button" :disabled="wikiBusy" @click="lintWiki">{{ t('wiki.lint') }}</button>
                <button type="button" class="stash-button is-primary" @click="newWikiPage">{{ t('wiki.newPage') }}</button>
              </div>
              <p v-if="wikiNotice" class="stash-wiki-notice" role="status">{{ t(wikiNotice) }}</p>
              <p v-if="wikiError" class="stash-error" role="alert">{{ t(wikiError) }}</p>
              <section v-if="wikiLint" class="stash-wiki-lint" :aria-label="t('wiki.lint')">
                <header><strong>{{ t('wiki.lint') }}</strong><span>{{ t('wiki.lintSummary', { pages: wikiLint.pages, findings: wikiLint.findings.length }) }}</span><button type="button" class="stash-button" @click="wikiLint = null">×</button></header>
                <p v-if="!wikiLint.findings.length">{{ t('wiki.lintClean') }}</p>
                <ul v-else><li v-for="(finding, index) in wikiLint.findings" :key="index" :data-severity="finding.severity"><button v-if="finding.page_slug" type="button" class="stash-wiki-linkbutton" @click="openWikiPage(finding.page_slug)">{{ finding.page_slug }}</button><span>{{ t('wiki.lint.' + finding.code) }}</span><code v-if="finding.target">{{ finding.target }}</code></li></ul>
              </section>
              <div v-if="!wikiPages.length" class="stash-empty"><strong>{{ filters.query || wikiFilters.kind || wikiFilters.stale ? t('wiki.noMatches') : t('wiki.noPages') }}</strong><span v-if="!filters.query">{{ t('wiki.noPagesHint') }}</span></div>
              <div v-else class="stash-wiki-list">
                <button v-for="page in wikiPages" :key="page.id" type="button" class="stash-wiki-card" @click="openWikiPage(page.slug)">
                  <span class="stash-wiki-card-head"><strong>{{ page.title }}</strong><span class="stash-wiki-kind" :data-kind="page.kind">{{ t('wiki.kind.' + page.kind) }}</span><span v-if="page.stale_at" class="stash-wiki-stale">{{ t('wiki.stale') }}</span></span>
                  <span v-if="page.summary" class="stash-wiki-card-summary">{{ page.summary }}</span>
                  <span class="stash-wiki-card-meta"><code>{{ page.slug }}</code><span>{{ t('wiki.updatedBy', { time: formatDateTime(page.updated_at), author: page.author || t('wiki.author.' + page.author_kind) }) }}</span><span v-for="tag in page.tags" :key="tag" class="stash-wiki-tag">{{ tag }}</span></span>
                </button>
              </div>
              <div v-if="wikiPages.length" class="stash-pagination"><span>{{ t('view.shownCount', { count: wikiPages.length }) }}</span><button v-if="route.offset" type="button" class="stash-button" @click="searchList">{{ t('action.firstPage') }}</button><button v-if="page.hasMore" type="button" class="stash-button" @click="nextPage">{{ t('action.nextPage') }}</button></div>
              <details class="stash-wiki-log" :open="!wikiPages.length"><summary>{{ t('wiki.log') }}</summary>
                <p v-if="!wikiLog.length">{{ t('wiki.logEmpty') }}</p>
                <ul v-else><li v-for="entry in wikiLog" :key="entry.id"><span class="stash-wiki-log-time">{{ formatDateTime(entry.created_at) }}</span><span class="stash-wiki-log-action" :data-action="entry.action">{{ t('wiki.action.' + entry.action) }}</span><button v-if="entry.page_slug" type="button" class="stash-wiki-linkbutton" @click="openWikiPage(entry.page_slug)">{{ entry.page_slug }}</button><span>{{ entry.summary }}</span><small v-if="entry.actor">{{ entry.actor }}</small></li></ul>
              </details>
            </div>
          </template>

          <template v-else-if="route.route === 'wiki_page' && wikiEdit">
            <form class="stash-wiki-editor" @submit.prevent="saveWikiPage">
              <div class="stash-wiki-editor-fields">
                <label class="stash-field"><span>{{ t('wiki.title') }}</span><input v-model="wikiEdit.title" required :disabled="wikiBusy"></label>
                <label class="stash-field"><span>{{ t('wiki.slug') }}</span><input v-model="wikiEdit.slug" required pattern="[a-z0-9][a-z0-9_-]*(/[a-z0-9][a-z0-9_-]*)*" :disabled="wikiBusy || !wikiEdit.isNew" :placeholder="t('wiki.slugHint')"></label>
                <label class="stash-field"><span>{{ t('wiki.kind') }}</span><select v-model="wikiEdit.kind" :disabled="wikiBusy"><option v-for="kind in wikiKinds" :key="kind" :value="kind">{{ t('wiki.kind.' + kind) }}</option></select></label>
                <label class="stash-field"><span>{{ t('wiki.tags') }}</span><input v-model="wikiEdit.tags" :disabled="wikiBusy"></label>
                <label class="stash-field is-wide"><span>{{ t('wiki.summary') }}</span><input v-model="wikiEdit.summary" :disabled="wikiBusy"></label>
              </div>
              <div class="stash-wiki-editor-body">
                <label class="stash-field"><span>{{ t('wiki.content') }}</span><textarea v-model="wikiEdit.content" required :disabled="wikiBusy" spellcheck="false"></textarea><small>{{ t('wiki.contentHint') }}</small></label>
                <section class="stash-wiki-preview" :aria-label="t('wiki.preview')"><h4>{{ t('wiki.preview') }}</h4><article class="stash-wiki-article" v-html="renderWikiMarkdown(wikiEdit.content)"></article></section>
              </div>
              <label class="stash-field"><span>{{ t('wiki.changeNote') }}</span><input v-model="wikiEdit.changeNote" :disabled="wikiBusy"></label>
              <p v-if="wikiNotice" class="stash-wiki-notice" role="status">{{ t(wikiNotice) }}</p>
              <p v-if="wikiError" class="stash-error" role="alert">{{ t(wikiError) }}</p>
              <div class="stash-llm-actions">
                <button type="submit" class="stash-button is-primary" :disabled="wikiBusy">{{ t('wiki.save') }}</button>
                <button type="button" class="stash-button" :disabled="wikiBusy" @click="cancelWikiEdit">{{ t('wiki.cancel') }}</button>
                <button type="button" class="stash-button" :disabled="wikiBusy" :title="t('wiki.compileHint')" @click="compileWikiDraft">{{ wikiBusy && wikiCompiling ? t('wiki.compiling') : t('wiki.compile') }}</button>
              </div>
            </form>
          </template>

          <template v-else-if="route.route === 'wiki_page' && wikiPage">
            <article class="stash-wiki-page">
              <header class="stash-wiki-page-head">
                <div><p class="stash-kicker"><code>{{ wikiPage.page.slug }}</code> · <span class="stash-wiki-kind" :data-kind="wikiPage.page.kind">{{ t('wiki.kind.' + wikiPage.page.kind) }}</span><span v-if="wikiPage.page.stale_at" class="stash-wiki-stale">{{ t('wiki.stale') }}</span></p><h2>{{ wikiPage.page.title }}</h2><p v-if="wikiPage.page.summary" class="stash-wiki-summary">{{ wikiPage.page.summary }}</p><p class="stash-wiki-meta">{{ t('wiki.revision', { revision: wikiPage.revision }) }} · {{ t('wiki.updatedBy', { time: formatDateTime(wikiPage.page.updated_at), author: wikiPage.page.author || t('wiki.author.' + wikiPage.page.author_kind) }) }} · {{ t('wiki.author.' + wikiPage.page.author_kind) }} · {{ wikiPage.page.indexed ? t('wiki.indexed') : t('wiki.unindexed') }}<span v-for="tag in wikiPage.page.tags" :key="tag" class="stash-wiki-tag">{{ tag }}</span></p></div>
                <div class="stash-llm-actions"><button type="button" class="stash-button" @click="navigate('wiki')">{{ t('action.backToList') }}</button><button type="button" class="stash-button" @click="toggleWikiHistory">{{ t('wiki.history') }}</button><button type="button" class="stash-button is-primary" :disabled="wikiBusy || wikiPage.revision !== wikiPage.page.revision" @click="editWikiPage">{{ t('wiki.edit') }}</button><button type="button" class="stash-button is-danger" :disabled="wikiBusy" @click="deleteWikiPage">{{ t('wiki.delete') }}</button></div>
              </header>
              <p v-if="wikiPage.revision !== wikiPage.page.revision" class="stash-context-note">{{ t('wiki.viewingRevision', { revision: wikiPage.revision, current: wikiPage.page.revision }) }} <button type="button" class="stash-button" @click="openWikiPage(wikiPage.page.slug)">{{ t('wiki.current') }}</button></p>
              <p v-if="wikiNotice" class="stash-wiki-notice" role="status">{{ t(wikiNotice) }}</p>
              <p v-if="wikiError" class="stash-error" role="alert">{{ t(wikiError) }}</p>
              <div class="stash-wiki-page-grid">
                <div class="stash-wiki-article" @click="wikiArticleClick" v-html="wikiRendered"></div>
                <aside class="stash-wiki-aside">
                  <section v-if="wikiHistoryOpen" :aria-label="t('wiki.history')"><h4>{{ t('wiki.history') }}</h4><p v-if="!wikiHistory.length">{{ t('wiki.historyEmpty') }}</p><ul v-else class="stash-wiki-history"><li v-for="revision in wikiHistory" :key="revision.id"><button type="button" class="stash-wiki-linkbutton" :class="{ 'is-active': revision.revision === wikiPage.revision }" @click="openWikiPage(wikiPage.page.slug, { revision: revision.revision })">{{ t('wiki.revision', { revision: revision.revision }) }}</button><span>{{ formatDateTime(revision.created_at) }} · {{ revision.author || t('wiki.author.' + revision.author_kind) }}</span><small v-if="revision.change_note">{{ revision.change_note }}</small></li></ul></section>
                  <section :aria-label="t('wiki.sources')"><h4>{{ t('wiki.sources') }}</h4><p v-if="!wikiPage.sources.length">{{ t('wiki.noSources') }}</p><ul v-else class="stash-wiki-sources"><li v-for="source in wikiPage.sources" :key="source.source_type + ':' + source.source_ref" :id="'source-' + source.source_type + ':' + source.source_ref" :class="{ 'is-focused': wikiFocusedSource === source.source_type + ':' + source.source_ref }"><div><code>{{ source.source_type }}:{{ source.source_ref }}</code><span class="stash-wiki-source-status" :data-status="source.status">{{ t('wiki.source.' + (source.status || 'ok')) }}</span></div><small v-if="source.excerpt">{{ source.excerpt }}</small><small v-if="source.note">{{ source.note }}</small></li></ul></section>
                  <section v-if="wikiPage.links.length" :aria-label="t('wiki.links')"><h4>{{ t('wiki.links') }}</h4><ul class="stash-wiki-linklist"><li v-for="link in wikiPage.links" :key="link.target_slug"><button type="button" class="stash-wiki-linkbutton" @click="openWikiPage(link.target_slug)">{{ link.target_title || link.target_slug }}</button><small v-if="!link.target_page_id">{{ t('wiki.missingTarget') }}</small></li></ul></section>
                  <section v-if="wikiPage.backlinks.length" :aria-label="t('wiki.backlinks')"><h4>{{ t('wiki.backlinks') }}</h4><ul class="stash-wiki-linklist"><li v-for="link in wikiPage.backlinks" :key="link.page_id"><button type="button" class="stash-wiki-linkbutton" @click="openWikiPage(link.target_slug)">{{ link.target_title || link.target_slug }}</button></li></ul></section>
                </aside>
              </div>
            </article>
          </template>

          <template v-else-if="route.route === 'graph'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="t('search.graph')" @input="syncURL"></label><label class="stash-field"><span>{{ t('field.status') }}</span><select v-model="filters.status" @change="syncURL"><option value="">{{ t('filter.allStatuses') }}</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><label class="stash-field"><span>{{ t('field.owner') }}</span><select v-model="filters.agent" @change="syncURL"><option value="">{{ t('filter.allOwners') }}</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label><button type="button" class="stash-button" @click="resetFilters">{{ t('action.reset') }}</button></div><div class="stash-checks"><span>{{ t('map.relationships') }}</span><label><input type="checkbox" v-model="relations.blocks" @change="syncURL">{{ t('map.blocks') }}</label><label><input type="checkbox" v-model="relations.part_of" @change="syncURL">{{ t('map.partOf') }}</label><label><input type="checkbox" v-model="relations.relates_to" @change="syncURL">{{ t('map.related') }}</label></div><div class="stash-legend"><button type="button" class="stash-button" @click="fitMap = !fitMap">{{ fitMap ? t('view.actualSize') : t('view.fit') }}</button><span><i style="color:#e9a23b"></i>{{ t('map.blockLegend') }}</span><span><i style="color:#9b8cf2"></i>{{ t('map.parentLegend') }}</span><span><i class="is-dash"></i>{{ t('map.related') }}</span></div>
            <div v-if="!graphLayout.nodes.length" class="stash-empty"><strong>{{ t('empty.graph') }}</strong></div>
            <div v-else class="stash-graph-viewport"><div class="stash-graph-canvas" :style="canvasStyle(graphLayout)"><svg class="stash-graph-edge-layer" :viewBox="'0 0 ' + graphLayout.width + ' ' + graphLayout.height" aria-hidden="true"><defs><marker id="stash-graph-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#e9a23b"></path></marker></defs><path v-for="edge in graphLayout.edges" :key="edge.key" :d="edge.path" :stroke="edge.stroke" stroke-width="2" :stroke-dasharray="edge.dashArray || null" :marker-end="edge.marker ? 'url(#stash-graph-arrow)' : null" fill="none"></path></svg><button v-for="node in graphLayout.nodes" :key="node.key" type="button" class="stash-graph-node" :class="{'is-selected': selected && selected.key === node.key}" :style="node.style" @click="selectGraphNode(node)"><span class="stash-node-meta"><span class="stash-node-key">{{ node.item.issue_key || '#' + node.item.id }}</span><span class="stash-status" :data-status="displayStatus(node.item)">{{ statusLabel(displayStatus(node.item)) }}</span></span><span class="stash-node-title">{{ node.item.title }}</span><span class="stash-node-note">{{ workNote(node.item) }}</span></button></div></div>
          </template>

          <template v-else-if="route.route === 'monitor'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="t('search.monitor')" @input="syncURL"></label><label class="stash-field"><span>{{ t('field.status') }}</span><select v-model="filters.status" @change="syncURL"><option value="">{{ t('filter.allStatuses') }}</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><label class="stash-field"><span>{{ t('field.owner') }}</span><select v-model="filters.agent" @change="syncURL"><option value="">{{ t('filter.allOwners') }}</option><option v-for="agent in agents" :key="agent" :value="agent">{{ agent }}</option></select></label><button type="button" class="stash-button" @click="resetFilters">{{ t('action.reset') }}</button></div><div class="stash-list"><button v-for="item in monitorRows" :key="item.id" type="button" class="stash-list-item" :class="{'is-selected': selected && selected.key === mapItemKey('work', item)}" @click="selectObject('work', item)"><span><strong>{{ item.issue_key || '#' + item.id }} · {{ item.title }}</strong><small>{{ workNote(item) }}</small></span><span class="stash-list-meta"><span class="stash-status" :data-status="displayStatus(item)">{{ statusLabel(displayStatus(item)) }}</span><span>{{ agentLabel(item) }}</span></span></button><div v-if="!monitorRows.length" class="stash-empty"><strong>{{ t('empty.work') }}</strong></div></div>
          </template>

          <template v-else-if="route.route === 'board'">
            <div class="stash-toolbar"><label class="stash-field is-search"><span>{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="t('search.work')" @input="syncURL" @keydown.enter="searchList"></label><label class="stash-field"><span>{{ t('field.type') }}</span><select v-model="filters.issueType" @change="searchList"><option value="">{{ t('filter.allTypes') }}</option><option value="task">{{ t('kind.work') }}</option><option value="bug">{{ t('issue.bug') }}</option><option value="feature">{{ t('issue.feature') }}</option><option value="chore">{{ t('issue.chore') }}</option><option value="question">{{ t('issue.question') }}</option></select></label><label class="stash-field"><span>{{ t('field.label') }}</span><input v-model="filters.label" :placeholder="t('field.label')" @input="syncURL" @keydown.enter="searchList"></label><button type="button" class="stash-button" @click="searchList">{{ t('action.search') }}</button><button type="button" class="stash-button" @click="resetFilters">{{ t('action.reset') }}</button></div><div v-if="!boardItems.length" class="stash-empty"><strong>{{ t('empty.work') }}</strong></div><div v-else class="stash-plan-grid"><div v-for="column in boardColumns" :key="column.status" class="stash-plan-component"><header><h3>{{ statusLabel(column.status) }}</h3><span class="stash-status" :data-status="column.status">{{ formatNumber(column.items.length) }}</span></header><ul class="stash-plan-tasks"><li v-for="item in column.items" :key="item.id" class="stash-plan-task"><button type="button" @click="selectObject('work', item)"><strong>{{ item.issue_key || '#' + item.id }}</strong> {{ item.title }}</button><small>{{ agentLabel(item) }}</small></li><li v-if="!column.items.length" class="stash-plan-task"><small>{{ t('empty.none') }}</small></li></ul></div></div>
          </template>

          <template v-else-if="route.route === 'plan'">
            <div v-if="planRootGoal" class="stash-plan-goal"><span>{{ t('plan.sharedGoal') }}</span><strong>{{ planRootGoal.content }}</strong><span class="stash-status">{{ goalProgressLabel(planRootGoal) }}</span></div><div v-if="plan.warnings.length || (plan.validation && (!plan.validation.passed || plan.validation.stale))" class="stash-context-note">{{ plan.warnings.length ? t('plan.warningCount', { count: plan.warnings.length }) + ' ' : '' }}{{ plan.validation && plan.validation.stale ? t('plan.stale') : plan.validation && !plan.validation.passed ? t('plan.reviewFailed') : '' }}</div><div v-if="!plan.components.length" class="stash-empty"><strong>{{ t('empty.plan') }}</strong></div><div v-else class="stash-plan-grid"><article v-for="component in plan.components" :key="component.id" class="stash-plan-component"><header><div><h3><button type="button" class="stash-plan-title" @click="selectObject('work', component)">{{ component.issue_key || '#' + component.id }} · {{ component.title }}</button></h3><p v-if="component.execution_progress">{{ componentProgressLabel(component) }}</p><p>{{ component.description || t('empty.conditions') }}</p></div><span class="stash-status" :data-status="displayStatus(component)">{{ statusLabel(displayStatus(component)) }}</span></header><ul class="stash-plan-tasks"><li v-for="task in component.tasks || []" :key="task.id" class="stash-plan-task"><button type="button" @click="selectObject('work', task)"><strong>{{ task.issue_key || '#' + task.id }}</strong> {{ task.title }}</button><small>{{ statusLabel(task.status) }}</small></li><li v-if="!(component.tasks || []).length" class="stash-plan-task"><small>{{ t('empty.children') }}</small></li></ul></article></div><div v-if="plan.decisions.length" class="stash-plan-decisions"><div v-for="decision in plan.decisions" :key="decision.id" class="stash-plan-decision"><strong>{{ decision.title }}</strong><div>{{ decision.rationale }}</div></div></div>
          </template>

          <template v-else-if="isListRoute">
            <div v-if="route.route === 'worktrees' && listItems.length" class="stash-git-actions"><button type="button" class="stash-button" :aria-expanded="gitFormOpen" @click="gitFormOpen = !gitFormOpen">{{ t('git.register') }}</button></div>
            <form v-if="route.route === 'worktrees' && gitFormOpen" class="stash-git-form" @submit.prevent="registerGitFolder"><h2>{{ t('git.register') }}</h2><label class="stash-field"><span>{{ t('git.repository') }}</span><input v-model="gitForm.repository" required autocomplete="off" :disabled="gitSaving"></label><label class="stash-field"><span>{{ t('git.folder') }}</span><input v-model="gitForm.worktree_path" required autocomplete="off" :disabled="gitSaving" placeholder="/path/to/project"></label><label class="stash-field"><span>{{ t('git.branch') }}</span><input v-model="gitForm.branch" autocomplete="off" :disabled="gitSaving"></label><p v-if="gitError" class="stash-error" role="alert">{{ t(gitError) }}</p><div class="stash-top-actions"><button class="stash-button is-primary" type="submit" :disabled="gitSaving">{{ gitSaving ? t('git.saving') : t('git.save') }}</button><button class="stash-button" type="button" :disabled="gitSaving" @click="gitFormOpen = false">{{ t('action.cancel') }}</button></div></form>
            <div v-if="route.route !== 'worktrees' || listItems.length || filters.query" class="stash-toolbar"><label class="stash-field is-search"><span>{{ t('action.search') }}</span><input v-model="filters.query" :placeholder="listPlaceholder" @input="syncURL" @keydown.enter="searchList"></label><label v-if="route.route === 'list_memories'" class="stash-field"><span>{{ t('field.type') }}</span><select v-model="filters.memoryType" @change="filters.status = ''; searchList()"><option value="">{{ t('filter.all') }}</option><option value="fact">{{ t('nav.facts') }}</option><option value="episode">{{ t('memory.episode') }}</option><option value="hypothesis">{{ t('nav.hypotheses') }}</option><option value="failure">{{ t('memory.failure') }}</option></select></label><label v-if="statusOptions.length" class="stash-field"><span>{{ t('field.status') }}</span><select v-model="filters.status" @change="searchList"><option value="">{{ t('filter.allStatuses') }}</option><option v-for="status in statusOptions" :key="status" :value="status">{{ statusLabel(status) }}</option></select></label><button type="button" class="stash-button" @click="searchList">{{ t('action.search') }}</button></div><div class="stash-list"><button v-for="item in visibleListItems" :key="listItemKey(item)" type="button" class="stash-list-item" :class="{'is-selected': selected && selected.key === listItemKey(item)}" @click="selectListItem(item)"><span><strong>{{ listItemTitle(item) }}</strong><small>{{ listItemSummary(item) }}</small></span><span class="stash-list-meta"><span v-if="listKind === 'memory'">{{ memoryTypeLabel(item.memory_type) }}</span><span v-if="item.status && (listKind !== 'memory' || item.memory_type === 'hypothesis')" class="stash-status" :data-status="item.status">{{ statusLabel(item.status) }}</span><span v-if="item.slug && route.route !== 'list_namespaces'">{{ item.slug }}</span></span></button><div v-if="!visibleListItems.length && !(route.route === 'worktrees' && gitFormOpen)" class="stash-empty"><template v-if="route.route === 'worktrees'"><strong>{{ filters.query ? t('empty.filtered') : t('git.empty') }}</strong><button v-if="filters.query" type="button" class="stash-button" @click="resetFilters">{{ t('action.clearFilters') }}</button><button v-else-if="!gitFormOpen" type="button" class="stash-button is-primary" @click="gitFormOpen = true">{{ t('git.register') }}</button></template><strong v-else>{{ t('empty.items') }}</strong></div></div>
          </template>

          <template v-else-if="route.route === 'tokens'">
            <section class="stash-token-page">
              <header><p>{{ t('tokens.description') }}</p></header>
              <form class="stash-token-form" @submit.prevent="issueToken">
                <label class="stash-field"><span>{{ t('tokens.name') }}</span><input v-model="tokenName" maxlength="120" autocomplete="off" :placeholder="t('tokens.namePlaceholder')" :disabled="tokenLoading"></label>
                <label class="stash-field"><span>{{ t('tokens.period') }}</span><select v-model="tokenPeriod" :disabled="tokenLoading"><option v-for="option in tokenPeriodOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select></label>
                <label v-if="tokenPeriod === 'custom'" class="stash-field"><span>{{ t('tokens.customDays') }}</span><input v-model.number="tokenCustomDays" type="number" min="1" max="106751" step="1" :placeholder="t('tokens.noExpiryHint')" :disabled="tokenLoading"></label>
                <button type="submit" class="stash-button is-primary" :disabled="tokenLoading">{{ tokenLoading ? t('auth.issuing') : t('tokens.create') }}</button>
              </form>
              <div v-if="issuedToken" class="stash-token-issued-page"><strong>{{ t('tokens.issued') }}</strong><span>{{ issuedTokenExpiresAt ? t('tokens.expiresAt', { time: formatDateTime(issuedTokenExpiresAt) }) : t('tokens.unlimited') }}</span><code>{{ issuedToken }}</code><button type="button" class="stash-button" @click="copyIssuedToken">{{ t('auth.copyToken') }}</button></div>
              <p v-if="tokenError" class="stash-error" role="alert">{{ t(tokenError) }}</p>
              <div v-if="authTokensLoading" class="stash-loading" role="status">{{ t('tokens.loading') }}</div>
              <div v-else-if="authTokensError" class="stash-error" role="alert">{{ t(authTokensError) }} <button type="button" class="stash-button" @click="loadAuthTokens">{{ t('action.retry') }}</button></div>
              <div v-else-if="!authTokens.length" class="stash-empty"><strong>{{ t('tokens.empty') }}</strong></div>
              <ul v-else class="stash-token-list"><li v-for="token in authTokens" :key="token.id" class="stash-token-row"><div><strong>{{ token.name || t('tokens.unnamed') }}</strong><small>#{{ token.id }} · {{ formatDateTime(token.created_at) }} · {{ t(tokenStatus(token)) }}<span v-if="token.last_used_at"> · {{ t('tokens.lastUsed', { time: formatDateTime(token.last_used_at) }) }}</span></small><small>{{ token.expires_at ? t('tokens.expiresAt', { time: formatDateTime(token.expires_at) }) : t('tokens.unlimited') }}</small></div><button v-if="!token.revoked_at" type="button" class="stash-button is-danger" :disabled="tokenRevokeID === token.id" @click="revokeToken(token)">{{ tokenRevokeID === token.id ? t('tokens.revoking') : t('tokens.revoke') }}</button></li></ul>
            </section>
          </template>
          <template v-else-if="route.route === 'agent'"><div class="stash-guide"><button type="button" class="stash-button" @click="copyAgentGuide">{{ t(copyStatus) }}</button><details><summary>{{ t('agent.allRules') }}</summary><textarea :aria-label="t('agent.rulesLabel')" readonly :value="agentGuide"></textarea></details></div></template>
          <template v-else-if="route.route === 'llm' && llm">
            <div class="stash-guide stash-llm">
              <p class="stash-llm-intro">{{ t('llm.description') }}</p>
              <p v-if="!llm.secrets_enabled" class="stash-llm-warning" role="status">{{ t('llm.noSecretsKey') }}</p>
              <p v-if="llmNotice" class="stash-llm-notice" role="status">{{ t(llmNotice) }}</p>
              <p v-if="llmError" class="stash-error" role="alert">{{ t(llmError) }}</p>
              <section class="stash-llm-section" :aria-label="t('llm.routes')">
                <header class="stash-llm-head"><h3>{{ t('llm.routes') }}</h3><button v-if="llm.environment_base_url" type="button" class="stash-button" :disabled="llmBusy" @click="importEnvironment">{{ t('llm.importEnv') }}</button></header>
                <div class="stash-llm-table-wrap"><table class="stash-llm-table"><thead><tr><th>{{ t('llm.feature') }}</th><th>{{ t('llm.provider') }}</th><th>{{ t('llm.model') }}</th><th>{{ t('llm.settings') }}</th><th></th></tr></thead>
                <tbody><tr v-for="info in llm.features" :key="info.feature">
                  <td class="stash-llm-feature"><strong>{{ t('llm.feature.' + info.feature) }}</strong><small>{{ t('llm.featureHint.' + info.feature) }}</small><span class="stash-llm-source" :data-source="llmRouteSource(info.feature)">{{ t('llm.source.' + llmRouteSource(info.feature)) }}<template v-if="llmRoute(info.feature) && llmRoute(info.feature).provider_name"> · {{ llmRoute(info.feature).provider_name }} / {{ llmRoute(info.feature).model }}</template></span><small v-if="llmRoute(info.feature) && llmRoute(info.feature).error" class="stash-llm-error">{{ llmRoute(info.feature).error }}</small></td>
                  <td><select v-model="llmAssignmentForms[info.feature].provider_id" :aria-label="t('llm.provider')" :disabled="llmBusy"><option value="">{{ llm.environment_base_url ? t('llm.useEnvironment') : t('llm.unassigned') }}</option><option v-for="provider in llm.providers" :key="provider.id" :value="String(provider.id)" :disabled="!provider.enabled">{{ provider.name }}</option></select></td>
                  <td><input v-model="llmAssignmentForms[info.feature].model" :aria-label="t('llm.model')" :list="'stash-llm-models-' + info.feature" :disabled="llmBusy || !llmAssignmentForms[info.feature].provider_id" :placeholder="t('llm.modelPlaceholder')"><datalist :id="'stash-llm-models-' + info.feature"><option v-for="model in llmModelOptions(info.feature)" :key="model" :value="model"></option></datalist></td>
                  <td class="stash-llm-numbers"><label v-if="info.kind === 'embedding'"><span>{{ t('llm.dimensions') }}</span><input v-model.number="llmAssignmentForms[info.feature].dimensions" type="number" min="1" max="2000" :disabled="llmBusy || !llmAssignmentForms[info.feature].provider_id"></label><label><span>{{ t('llm.contextTokens') }}</span><input v-model.number="llmAssignmentForms[info.feature].context_tokens" type="number" min="0" :disabled="llmBusy || !llmAssignmentForms[info.feature].provider_id"></label><label v-if="info.kind === 'reasoning'"><span>{{ t('llm.reservedTokens') }}</span><input v-model.number="llmAssignmentForms[info.feature].reserved_tokens" type="number" min="0" :disabled="llmBusy || !llmAssignmentForms[info.feature].provider_id"></label></td>
                  <td><button type="button" class="stash-button is-primary" :disabled="llmBusy" @click="saveAssignment(info.feature)">{{ t('llm.save') }}</button></td>
                </tr></tbody></table></div>
              </section>
              <section class="stash-llm-section" :aria-label="t('llm.providers')">
                <header class="stash-llm-head"><h3>{{ t('llm.providers') }}</h3><button type="button" class="stash-button" :disabled="llmBusy" @click="openProviderForm(null)">{{ t('llm.addProvider') }}</button></header>
                <div v-if="!llm.providers.length && !llmProviderForm" class="stash-empty"><strong>{{ t('llm.noProviders') }}</strong><span>{{ t('llm.noProvidersHint') }}</span></div>
                <ul v-else-if="llm.providers.length" class="stash-llm-providers">
                  <li v-for="provider in llm.providers" :key="provider.id" :class="{ 'is-disabled': !provider.enabled }">
                    <div><strong>{{ provider.name }}</strong><small>{{ provider.base_url }} · {{ provider.has_api_key ? t('llm.hasKey') : t('llm.noKey') }} · {{ t('llm.timeout', { seconds: provider.request_timeout_seconds }) }}<template v-if="!provider.enabled"> · {{ t('llm.disabled') }}</template></small><small v-if="llmProbe[String(provider.id)]" :class="{ 'stash-llm-error': !llmProbe[String(provider.id)].ok }">{{ llmProbeText(llmProbe[String(provider.id)]) }}</small></div>
                    <div class="stash-llm-actions"><button type="button" class="stash-button" :disabled="llmBusy" @click="probeProvider(provider)">{{ t('llm.probe') }}</button><button type="button" class="stash-button" :disabled="llmBusy" @click="openProviderForm(provider)">{{ t('llm.edit') }}</button><button type="button" class="stash-button is-danger" :disabled="llmBusy" @click="deleteProvider(provider)">{{ t('llm.delete') }}</button></div>
                  </li>
                </ul>
                <form v-if="llmProviderForm" class="stash-llm-form" @submit.prevent="saveProvider">
                  <h4>{{ llmProviderForm.id ? t('llm.editProvider') : t('llm.addProvider') }}</h4>
                  <label class="stash-field"><span>{{ t('llm.name') }}</span><input v-model="llmProviderForm.name" required pattern="[a-z0-9][a-z0-9_-]{0,63}" autocomplete="off" :disabled="llmBusy" placeholder="openai"></label>
                  <label class="stash-field"><span>{{ t('llm.baseUrl') }}</span><input v-model="llmProviderForm.base_url" required type="url" autocomplete="off" :disabled="llmBusy" placeholder="https://api.openai.com/v1"></label>
                  <label class="stash-field"><span>{{ t('llm.apiKey') }}</span><input v-model="llmProviderForm.api_key" type="password" autocomplete="off" :disabled="llmBusy || !llm.secrets_enabled" :placeholder="llmProviderForm.keep_key ? t('llm.keepKey') : t('llm.apiKeyOptional')"></label>
                  <label v-if="llmProviderForm.id && llmProviderForm.keep_key" class="stash-check"><input v-model="llmProviderForm.clear_key" type="checkbox" :disabled="llmBusy"><span>{{ t('llm.clearKey') }}</span></label>
                  <label class="stash-field"><span>{{ t('llm.timeoutSeconds') }}</span><input v-model.number="llmProviderForm.request_timeout_seconds" type="number" min="1" :disabled="llmBusy"></label>
                  <label class="stash-check"><input v-model="llmProviderForm.enabled" type="checkbox" :disabled="llmBusy"><span>{{ t('llm.enabled') }}</span></label>
                  <small v-if="llmProbe.form" :class="{ 'stash-llm-error': !llmProbe.form.ok }">{{ llmProbeText(llmProbe.form) }}</small>
                  <div class="stash-llm-actions"><button type="button" class="stash-button" :disabled="llmBusy" @click="probeProvider(llmProviderForm)">{{ t('llm.probe') }}</button><button type="submit" class="stash-button is-primary" :disabled="llmBusy">{{ t('llm.save') }}</button><button type="button" class="stash-button" :disabled="llmBusy" @click="llmProviderForm = null">{{ t('action.cancel') }}</button></div>
                </form>
              </section>
            </div>
          </template>
          <template v-else-if="route.route === 'maintenance' && maintenance"><div class="stash-guide"><p v-if="maintenance.provider_available === false" class="stash-llm-warning" role="status">{{ t('maintenance.noProvider') }}</p><dl class="stash-maintenance-counts"><div><dt>{{ t('maintenance.pending') }}</dt><dd>{{ formatNumber(maintenance.pending || 0) }}</dd></div><div><dt>{{ t('maintenance.due') }}</dt><dd>{{ formatNumber(maintenance.due || 0) }}</dd></div><div><dt>{{ t('maintenance.failed') }}</dt><dd>{{ formatNumber(maintenance.failed || 0) }}</dd></div><div><dt>{{ t('maintenance.paused') }}</dt><dd>{{ formatNumber(maintenance.paused || 0) }}</dd></div></dl><div class="stash-top-actions"><button type="button" class="stash-button" :disabled="maintenanceAction" @click="runMaintenance('retry')">{{ t('maintenance.retry') }}</button><button type="button" class="stash-button" :disabled="maintenanceAction" @click="runMaintenance('reindex')">{{ t('maintenance.reindex') }}</button></div><p v-if="maintenanceNotice" role="status">{{ t(maintenanceNotice) }}</p><details v-if="maintenance.latest_error"><summary>{{ t('maintenance.latestError') }}</summary><p>{{ maintenance.latest_error }}</p></details></div></template>
          <template v-else><div class="stash-empty"><strong>{{ staticTitle }}</strong><span>{{ staticText }}</span></div></template>
          <div v-if="((isListRoute && route.route !== 'list_namespaces') || route.route === 'board') && listItems.length" class="stash-pagination"><span>{{ t('view.shownCount', { count: listItems.length }) }}</span><button v-if="route.offset" type="button" class="stash-button" @click="searchList">{{ t('action.firstPage') }}</button><button v-if="page.hasMore" type="button" class="stash-button" @click="nextPage">{{ t('action.more') }}</button></div>
        </section>

        <aside v-if="selected && !route.detail" class="stash-inspector" tabindex="-1" :aria-label="t('view.selection')">
          <div class="stash-inspector-head"><div><p class="stash-kicker">{{ kindLabel(selected.kind) }}</p><h3>{{ selectedTitle }}</h3></div><button type="button" :aria-label="t('action.closeSelection')" @click="clearSelection">×</button></div>
          <p v-if="selectionLoading" class="stash-selection-loading" role="status">{{ t('view.loadingDetail') }}</p><dl><div v-for="field in selectedFields.slice(0, 6)" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div></dl>
          <div v-if="selectedParent || selectedChildren.length" class="stash-related-links"><div v-if="selectedParent"><span>{{ t('view.parentTitle', { kind: kindLabel(selectedParent.kind) }) }}</span><button type="button" @click="selectObject(selectedParent.kind, selectedParent.item)">{{ itemTitle(selectedParent.kind, selectedParent.item) }}</button></div><div v-if="selectedChildren.length"><span>{{ t('view.childCount', { count: selectedChildren.length }) }}</span><div><button v-for="child in selectedChildren" :key="child.key" type="button" @click="selectObject(child.kind, child.item)">{{ itemTitle(child.kind, child.item) }}</button></div></div></div>
          <div v-if="selectedConnections.length" class="stash-related-links" :aria-label="t('view.connections')"><div v-for="connection in selectedConnections" :key="connection.key"><span>{{ connection.label }}</span><button type="button" @click="selectObject(connection.kind, connection.item)">{{ itemTitle(connection.kind, connection.item) }}</button></div></div>
          <div class="stash-inspector-actions"><button type="button" class="stash-button is-primary" @click="openDetail">{{ t('action.details') }}</button><a v-if="selected.kind === 'work'" class="stash-button" :href="issueHref(selected.item)">{{ t('action.showBoard') }}</a></div>
        </aside>
      </div>
    </section>
  </main>
</div>`;

    const viewModel = root.StashVueConsoleViewModel.createViewModel({
        api: root.StashApiClient.createApiClient(),
        routeAPI: root.StashRouteState,
        goalMap: root.StashGoalMap,
        workGraph: root.StashWorkGraph,
        search: root.StashSearch,
        i18n: root.StashI18n,
        window: root
    });
    let selectionTrigger = null;
    const focusSelection = function () {
        this.$nextTick(() => {
            const target = document.querySelector(this.route.detail ? '.stash-detail' : '.stash-inspector');
            if (target) target.focus();
        });
    };
    root.StashVueRuntime.createApp({ ...viewModel, template, watch: {
        selected(next, previous) {
            if (next && !previous) selectionTrigger = document.activeElement;
            if (next) focusSelection.call(this);
            else if (selectionTrigger && selectionTrigger.isConnected) selectionTrigger.focus();
        },
        'route.detail': focusSelection,
        refreshRevision() {
            // Capture immediately before Vue patches, so scrolling during a request is preserved.
            const position = [window.scrollX, window.scrollY];
            const panes = [...document.querySelectorAll('.stash-inspector, .stash-map-viewport, .stash-graph-viewport')].map(element => [element, element.scrollLeft, element.scrollTop]);
            this.$nextTick(() => {
                window.scrollTo(...position);
                for (const [element, left, top] of panes) if (element.isConnected) { element.scrollLeft = left; element.scrollTop = top; }
            });
        }
    } })
        .mount(document.querySelector('[data-stash-vue-console]'));
}(typeof globalThis !== 'undefined' ? globalThis : window));
