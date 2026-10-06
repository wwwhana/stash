# The Stash Wiki

Memory is raw. Episodes are what happened; facts are what consolidation
concluded from them. The wiki is what a project currently believes, written
down as Markdown pages that cite the memory and work they were built from.
People read it in the console, agents read and write it over MCP, and
`stash wiki export` turns a namespace into a folder of `.md` files.

The flow Stash is built around is: agents record what happens (episodes),
work is tracked and finished (work items, `finish_work`), and the result is
written up where the next person or agent will look for it (a wiki page that
cites `[@work:W-000123]` and the facts behind it).

## Pages

Each namespace has its own wiki. A page has:

| Field | Meaning |
| --- | --- |
| `slug` | Address inside the namespace: lowercase segments separated by `/`, such as `ops/deploys` or `decisions/postgres-16`. |
| `title`, `summary` | Shown in lists and search results. |
| `kind` | `article` (default), `index` (links the main pages), `entity` (one thing), `decision` (why something was decided), `log` (running notes). |
| `tags` | Lowercase labels for filtering. |
| `content` | Markdown. `[[slug]]` or `[[slug|label]]` links another page; `[@fact:12]`, `[@episode:3]`, `[@hypothesis:4]`, `[@failure:2]`, `[@goal:7]`, `[@work:W-000123]`, `[@page:index]`, and `[@url:https://…]` cite evidence. |
| `revision` | Increments on every write; the full history is kept. |
| `author`, `author_kind` | Who wrote the current revision: `agent`, `human` (console or CLI), or `server` (`wiki_compile`). |
| `stale_at` | Set by lint when cited evidence changed, was superseded, or was deleted. |

Citations are resolved on every read, so a page always shows whether its
sources are `ok`, `changed`, `superseded` (a fact with a newer value),
`deleted`, or `missing`. Sources outside the page's namespace tree resolve as
missing; cite within the project.

## Search without a model

Pages are searched with PostgreSQL trigram matching over the title, summary,
and body, so the wiki works with no embedding provider at all. When one is
assigned, pages are vectorized like memories and both signals are fused.
`recall` returns pages next to episodes and facts by default (`include_pages`),
and a page result carries `slug` and `title`.

## Who writes

- **Agents** write with `wiki_write`. The bundled `stash-wiki` skill
  (served through the MCP skills extension and also in `docs/AGENT.md`)
  tells them when: after `finish_work`, after a decision, when the same
  question was answered from memory twice, or when a page proved wrong.
- **People** edit in the console (`/ui/wiki`, the default page) with a live
  preview, or with `stash wiki write --file page.md`.
- **The server** can draft. `wiki_compile` gathers memory for a topic (or the
  pinned `sources`), asks the model assigned to the `wiki` feature for a cited
  draft, rejects drafts that cite anything it was not given, and either
  returns the draft or saves it as a `server`-authored page. Assign the model
  in **Model settings**; without one, `wiki_compile` says so and agents or
  people write the page themselves.

Concurrent edits are safe: pass the page's `revision` as `expected_revision`
and a stale write is rejected instead of overwriting someone else's text.
Fields left out of an update (`tags`, `summary`, `kind`) keep their stored
values.

## Lint

`wiki_lint` (or `stash wiki lint`) reports:

| Code | Meaning | Fix |
| --- | --- | --- |
| `broken_link` | `[[slug]]` points at a page that does not exist | create or correct the page |
| `orphan_page` | nothing links to the page | link it from the index or a related page |
| `stale_source` | cited evidence changed, was superseded, or was deleted | re-read the source and update the text |
| `missing_source` | a citation cannot be found in this namespace | fix the id or remove the citation |
| `thin_page` | almost no content | write it or delete it |
| `missing_index` | no `index` page | write one that links the main pages |

Lint also sets or clears `stale_at`, so `wiki_list` with `stale: true` shows
the pages that need a second look.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `wiki_search` | Hybrid search over pages across namespaces. |
| `wiki_read` | One page with links, backlinks, and resolved sources. Content comes in windows (`offset`, `next_offset`, `snapshot`). `revision` reads history. |
| `wiki_list` | Pages in a namespace, filtered by keyword, kind, tag, or stale. |
| `wiki_write` | Create or update a page; `expected_revision` guards concurrent edits. |
| `wiki_delete` | Hide a page; `restore: true` brings it back. |
| `wiki_history` | A page's revisions. |
| `wiki_log` | The namespace's wiki activity. |
| `wiki_lint` | The checks above. |
| `wiki_compile` | A cited server draft from memory. |

## CLI

```bash
stash wiki list --namespace /projects/myapp --q deploy
stash wiki read --namespace /projects/myapp ops/deploys
stash wiki write --namespace /projects/myapp ops/deploys --title "Deploys" --file deploys.md --tag ops --source work:W-000123
stash wiki lint --namespace /projects/myapp
stash wiki export --namespace /projects/myapp --dir ./wiki   # <slug>.md with front matter + README.md
stash wiki compile --namespace /projects/myapp ops/deploys --topic "staging deploy failures" --save
```

Exported files carry YAML front matter (`title`, `slug`, `kind`, `summary`,
`tags`, `revision`, `updated_at`, `sources`) followed by the Markdown body,
so a folder can be committed to git or served by any static site tool.
