// The console compiles its in-page templates at runtime, so it needs the full
// Vue build. Expose only the factory it uses; the console owns everything else.
import { createApp } from 'vue';

window.StashVueRuntime = { createApp };
