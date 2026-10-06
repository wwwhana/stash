---
name: stash-wiki
description: Keep the Stash wiki, the readable and cited knowledge layer over memory, current. Use it to answer project questions from compiled pages, to record durable results after work finishes, and to lint the wiki. Works without an embedding provider; pages are found by keyword.
license: Apache-2.0
metadata:
  author: Stash
  version: "1.0.0"
---

# Stash Wiki

The wiki turns raw memory into pages that people and agents read. Every page cites the episodes, facts, and work it was compiled from, so a reader can always get back to the evidence. Episodes are what happened; facts are what consolidation concluded; wiki pages are what the project currently believes, written down.

## Read before answering

1. For a question about the project, call `wiki_search` (or `recall` with `include_pages: true`) first. A page result carries `slug` and `title`; read it with `wiki_read`.
2. Prefer a page over raw episodes when both match: the page is the reviewed synthesis. Follow its `sources` when the exact evidence matters and its `[[links]]` for related pages.
3. Treat a page with `stale_at`, or a source marked `changed`, `superseded`, or `deleted`, as needing a re-check before relying on it.
4. `wiki_read` returns bounded windows. Continue with `offset: next_offset` and pass `snapshot` back while `has_more` is true.

## Write after durable results

- Update the wiki when work finishes (`finish_work`), when a decision is made, when a question was answered from memory for the second time, or when a page proved wrong.
- One page per subject: an entity (a service, a dataset), a decision (`decisions/<topic>`), a runbook, or a project overview. Keep an `index` page (`kind: index`) that links the main pages.
- Write Markdown with a one-paragraph lead, `##` sections, `[[slug]]` links to related pages, and `[@fact:12]`, `[@episode:3]`, or `[@work:W-000123]` citations right after the claims they support. Take the ids from `recall`, `query_facts`, `resume_work`, or `finish_work`.
- Call `wiki_write` with the exact `namespace`, a lowercase `slug` (`ops/deploys`, `decisions/postgres-16`), a `title`, the full `content`, a short `summary`, and a `change_note`. To update, read the page first and pass its `revision` as `expected_revision`; a conflict means someone else wrote first, so read again and merge.
- Omitted `tags`, `summary`, and `kind` keep their stored values on an update. Send `tags` explicitly to change them.
- Never paste secrets, tokens, lease tokens, or whole external documents into a page. Cite `[@url:...]` or a work resource instead.

## Keep it healthy

- Run `wiki_lint` after a batch of writes. Fix `broken_link` by creating or correcting the page, `orphan_page` by linking from the index, `stale_source` by re-reading the source and updating the text, and `missing_index` by writing an index page.
- `wiki_delete` hides a wrong or obsolete page; `wiki_delete` with `restore: true` brings it back. `wiki_history` and `wiki_read` with `revision` show earlier text.
- `wiki_compile` asks the server's wiki model to draft or refresh a page from memory with citations. It needs a reasoning provider on the `wiki` feature; without one, write the page yourself.

## Flow: work, then todo, then wiki

After `finish_work` reports `result_memory_linked: true`, update the project's page (or create `projects/<name>`) with what was delivered, citing `[@work:<issue_key>]` and the memory it relied on, and link any decision page the work produced. The Goal Map shows the work; the wiki page is where the result is explained.
