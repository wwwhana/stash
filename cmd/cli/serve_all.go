package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

func serveAllCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)
	consolidation := consolidationOptionsFromCommand(cmd)
	return serveMCPHTTP(ctx, bc, mcpHTTPOptions{
		Addr:          configuredListenAddress(bc, cmd),
		Consolidation: &consolidation,
	})
}
