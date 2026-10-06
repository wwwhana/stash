package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/urfave/cli/v3"
)

// wikiCommand reads and edits wiki pages from the server host. It is the
// human's door into the same pages agents maintain over MCP, and export
// turns a namespace into a folder of Markdown files for git or a browser.
func wikiCommand() *cli.Command {
	namespaceFlag := &cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Exact namespace path", Required: true}
	return &cli.Command{
		Name:  "wiki",
		Usage: "Read, write, lint, and export the wiki built over memory",
		Commands: []*cli.Command{
			{
				Name: "list", Usage: "List pages", Action: wikiListCmd,
				Flags: []cli.Flag{namespaceFlag, &cli.StringFlag{Name: "q"}, &cli.StringFlag{Name: "kind"}, &cli.StringFlag{Name: "tag"}, &cli.BoolFlag{Name: "stale", Usage: "Only pages whose evidence changed"},
					&cli.IntFlag{Name: "limit", Value: 100}, &cli.IntFlag{Name: "offset"}},
			},
			{
				Name: "read", Usage: "Print a page as Markdown", ArgsUsage: "<slug>", Action: wikiReadCmd,
				Flags: []cli.Flag{namespaceFlag, &cli.IntFlag{Name: "revision", Usage: "Historical revision number"}, &cli.BoolFlag{Name: "json", Usage: "Print the page document as JSON"}},
			},
			{
				Name: "write", Usage: "Create or update a page from a file or inline text", ArgsUsage: "<slug>", Action: wikiWriteCmd,
				Flags: []cli.Flag{namespaceFlag,
					&cli.StringFlag{Name: "title", Required: true}, &cli.StringFlag{Name: "file", Usage: "Markdown file to read the content from (- for stdin)"},
					&cli.StringFlag{Name: "content", Usage: "Inline Markdown content"}, &cli.StringFlag{Name: "summary"}, &cli.StringFlag{Name: "kind"},
					&cli.StringSliceFlag{Name: "tag"}, &cli.StringSliceFlag{Name: "source", Usage: "Evidence such as fact:12 or work:W-000001"},
					&cli.StringFlag{Name: "note", Usage: "Change note"}, &cli.IntFlag{Name: "expected-revision"}, &cli.StringFlag{Name: "author", Value: "cli"}},
			},
			{Name: "delete", Usage: "Hide a page (restorable)", ArgsUsage: "<slug>", Action: wikiDeleteCmd, Flags: []cli.Flag{namespaceFlag, &cli.BoolFlag{Name: "restore", Usage: "Restore instead of delete"}}},
			{Name: "history", Usage: "List a page's revisions", ArgsUsage: "<slug>", Action: wikiHistoryCmd, Flags: []cli.Flag{namespaceFlag, &cli.IntFlag{Name: "limit", Value: 50}}},
			{Name: "lint", Usage: "Report broken links, orphans, stale evidence, and a missing index", Action: wikiLintCmd, Flags: []cli.Flag{namespaceFlag}},
			{Name: "log", Usage: "Show recent wiki activity", Action: wikiLogCmd, Flags: []cli.Flag{namespaceFlag, &cli.IntFlag{Name: "limit", Value: 50}}},
			{
				Name: "export", Usage: "Write every page as <dir>/<slug>.md with front matter", Action: wikiExportCmd,
				Flags: []cli.Flag{namespaceFlag, &cli.StringFlag{Name: "dir", Usage: "Output directory", Required: true}},
			},
			{
				Name: "compile", Usage: "Ask the wiki reasoner to draft a page from memory", ArgsUsage: "<slug>", Action: wikiCompileCmd,
				Flags: []cli.Flag{namespaceFlag, &cli.StringFlag{Name: "topic"}, &cli.StringFlag{Name: "title"}, &cli.StringSliceFlag{Name: "source"},
					&cli.IntFlag{Name: "max-sources", Value: 20}, &cli.BoolFlag{Name: "save", Usage: "Store the draft as a server-authored page"}, &cli.StringFlag{Name: "note"}},
			},
		},
	}
}

func wikiNamespace(ctx context.Context, cmd *cli.Command) (int64, error) {
	_, namespaceID, err := exactNamespaceID(ctx, getBootstrap(cmd), cmd.String("namespace"))
	return namespaceID, err
}

func wikiSlugArg(cmd *cli.Command) (string, error) {
	if cmd.Args().Len() != 1 {
		return "", fmt.Errorf("page slug is required")
	}
	return cmd.Args().First(), nil
}

func wikiListCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	filter := brain.WikiListFilter{Query: cmd.String("q"), Kind: cmd.String("kind"), Tag: cmd.String("tag")}
	if cmd.Bool("stale") {
		stale := true
		filter.Stale = &stale
	}
	pages, err := getBootstrap(cmd).Brain.ListWikiPages(ctx, namespaceID, filter, brain.Pagination{Limit: int(cmd.Int("limit")), Offset: int(cmd.Int("offset"))})
	if err != nil {
		return err
	}
	return printJSON(pages)
}

func wikiReadCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	slug, err := wikiSlugArg(cmd)
	if err != nil {
		return err
	}
	bc := getBootstrap(cmd)
	detail, err := bc.Brain.GetWikiPage(ctx, namespaceID, slug)
	if err != nil {
		return err
	}
	if revision := int(cmd.Int("revision")); revision > 0 && revision != detail.Page.Revision {
		historical, err := bc.Brain.GetWikiRevision(ctx, namespaceID, slug, revision)
		if err != nil {
			return err
		}
		detail.Page.Title, detail.Page.Summary, detail.Page.Content, detail.Page.Revision = historical.Title, historical.Summary, historical.Content, historical.Revision
	}
	if cmd.Bool("json") {
		return printJSON(detail)
	}
	fmt.Print(wikiMarkdownDocument(detail))
	return nil
}

func wikiContentFlag(cmd *cli.Command) (string, error) {
	if file := cmd.String("file"); file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = readAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return "", fmt.Errorf("read content: %w", err)
		}
		return string(data), nil
	}
	if content := cmd.String("content"); strings.TrimSpace(content) != "" {
		return content, nil
	}
	return "", fmt.Errorf("--file or --content is required")
}

func wikiWriteCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	slug, err := wikiSlugArg(cmd)
	if err != nil {
		return err
	}
	content, err := wikiContentFlag(cmd)
	if err != nil {
		return err
	}
	input := brain.WikiPageInput{
		Slug: slug, Title: cmd.String("title"), Kind: cmd.String("kind"), Summary: cmd.String("summary"), Content: content,
		Author: cmd.String("author"), AuthorKind: "human", ChangeNote: cmd.String("note"), ExpectedRevision: int(cmd.Int("expected-revision")),
	}
	if cmd.IsSet("tag") {
		input.Tags = cmd.StringSlice("tag")
	}
	for _, ref := range cmd.StringSlice("source") {
		parsed, err := brain.ParseWikiSourceRef(ref)
		if err != nil {
			return err
		}
		input.Sources = append(input.Sources, parsed)
	}
	detail, created, err := getBootstrap(cmd).Brain.WriteWikiPage(ctx, namespaceID, input)
	if err != nil {
		return err
	}
	return printJSON(wikiResponse(detail, created))
}

func wikiDeleteCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	slug, err := wikiSlugArg(cmd)
	if err != nil {
		return err
	}
	bc := getBootstrap(cmd)
	if cmd.Bool("restore") {
		if err := bc.Brain.RestoreWikiPage(ctx, namespaceID, slug, "cli", "human"); err != nil {
			return err
		}
		return printJSON(map[string]any{"slug": slug, "restored": true})
	}
	if err := bc.Brain.DeleteWikiPage(ctx, namespaceID, slug, "cli", "human"); err != nil {
		return err
	}
	return printJSON(map[string]any{"slug": slug, "deleted": true})
}

func wikiHistoryCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	slug, err := wikiSlugArg(cmd)
	if err != nil {
		return err
	}
	revisions, err := getBootstrap(cmd).Brain.WikiPageRevisions(ctx, namespaceID, slug, brain.Pagination{Limit: int(cmd.Int("limit"))})
	if err != nil {
		return err
	}
	return printJSON(revisions)
}

func wikiLintCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	report, err := getBootstrap(cmd).Brain.LintWiki(ctx, namespaceID, "cli", "human")
	if err != nil {
		return err
	}
	return printJSON(report)
}

func wikiLogCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	entries, err := getBootstrap(cmd).Brain.WikiLog(ctx, namespaceID, brain.Pagination{Limit: int(cmd.Int("limit"))})
	if err != nil {
		return err
	}
	return printJSON(entries)
}

func wikiCompileCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	slug, err := wikiSlugArg(cmd)
	if err != nil {
		return err
	}
	result, err := getBootstrap(cmd).Brain.CompileWikiPage(ctx, namespaceID, brain.WikiCompileRequest{
		Slug: slug, Title: cmd.String("title"), Topic: cmd.String("topic"), SourceRefs: cmd.StringSlice("source"),
		MaxSources: int(cmd.Int("max-sources")), Save: cmd.Bool("save"), Author: "cli", ChangeNote: cmd.String("note"),
	})
	if err != nil {
		return err
	}
	return printJSON(result)
}

// wikiExportCmd writes the namespace as a folder of Markdown files so the
// wiki can be read in an editor, committed to git, or served statically.
func wikiExportCmd(ctx context.Context, cmd *cli.Command) error {
	namespaceID, err := wikiNamespace(ctx, cmd)
	if err != nil {
		return err
	}
	bc := getBootstrap(cmd)
	dir := cmd.String("dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	var written []string
	for offset := 0; ; offset += 200 {
		pages, err := bc.Brain.ListWikiPages(ctx, namespaceID, brain.WikiListFilter{}, brain.Pagination{Limit: 200, Offset: offset})
		if err != nil {
			return err
		}
		for _, page := range pages {
			detail, err := bc.Brain.GetWikiPage(ctx, namespaceID, page.Slug)
			if err != nil {
				return err
			}
			target := filepath.Join(dir, filepath.FromSlash(page.Slug)+".md")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
			}
			if err := os.WriteFile(target, []byte(wikiMarkdownDocument(detail)), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			written = append(written, page.Slug)
		}
		if len(pages) < 200 {
			break
		}
	}
	sort.Strings(written)
	var index strings.Builder
	index.WriteString("# Wiki export\n\n")
	for _, slug := range written {
		fmt.Fprintf(&index, "- [%s](%s.md)\n", slug, slug)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(index.String()), 0o644); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}
	return printJSON(map[string]any{"dir": dir, "pages": len(written)})
}

// wikiMarkdownDocument renders a page with YAML front matter that round-trips
// the metadata a reader or importer needs.
func wikiMarkdownDocument(detail *models.WikiPageDetail) string {
	page := detail.Page
	var doc strings.Builder
	doc.WriteString("---\n")
	fmt.Fprintf(&doc, "title: %s\nslug: %s\nkind: %s\nrevision: %d\nupdated_at: %s\n", yamlQuote(page.Title), page.Slug, page.Kind, page.Revision, page.UpdatedAt.UTC().Format(time.RFC3339))
	if page.Summary != "" {
		fmt.Fprintf(&doc, "summary: %s\n", yamlQuote(page.Summary))
	}
	if len(page.Tags) > 0 {
		quoted := make([]string, 0, len(page.Tags))
		for _, tag := range page.Tags {
			quoted = append(quoted, yamlQuote(tag))
		}
		fmt.Fprintf(&doc, "tags: [%s]\n", strings.Join(quoted, ", "))
	}
	if len(detail.Sources) > 0 {
		doc.WriteString("sources:\n")
		for _, source := range detail.Sources {
			fmt.Fprintf(&doc, "  - %s\n", yamlQuote(source.SourceType+":"+source.SourceRef))
		}
	}
	doc.WriteString("---\n\n")
	doc.WriteString(page.Content)
	if !strings.HasSuffix(page.Content, "\n") {
		doc.WriteString("\n")
	}
	return doc.String()
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}

func readAll(reader io.Reader) ([]byte, error) {
	return io.ReadAll(reader)
}
