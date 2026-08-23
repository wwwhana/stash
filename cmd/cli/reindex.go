package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// reindexCmd re-embeds all stored episodes and facts.
// Content is left untouched — only the embedding column is rewritten.
func reindexCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)
	dryRun := cmd.Bool("dry-run")

	res, err := bc.Brain.Reindex(ctx, dryRun, func(table string, done, total int) {
		fmt.Printf("  %s: %d/%d\n", table, done, total)
	})
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("dry run: %d episodes, %d facts would be re-embedded\n",
			res.EpisodesTotal, res.FactsTotal)
		return nil
	}

	fmt.Printf("reindex complete: episodes %d/%d, facts %d/%d, failed %d\n",
		res.EpisodesDone, res.EpisodesTotal, res.FactsDone, res.FactsTotal, res.Failed)
	return nil
}
