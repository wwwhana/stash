// The console compiles its in-page templates at runtime, so it needs the full
// Vue build. Markdown rendering for wiki pages ships here too so the console
// stays a single self-hosted bundle under its strict CSP.
import { createApp } from 'vue';
import { marked } from 'marked';
import DOMPurify from 'dompurify';

window.StashVueRuntime = { createApp, marked, DOMPurify };
