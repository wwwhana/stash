package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/urfave/cli/v3"
)

func serveAllCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)

	httpHost := cmd.String("http-host")
	httpPort := cmd.String("http-port")
	mcpHost := cmd.String("mcp-host")
	mcpPort := cmd.String("mcp-port")
	consolidateInterval := cmd.Duration("consolidate-interval")
	consolidateNamespaces := cmd.StringSlice("consolidate-namespaces")
	if bc != nil && bc.Config != nil {
		configuredHost, configuredPort := configuredHTTPAddress(bc.Config.HTTPAddr, httpHost, httpPort)
		if !cmd.IsSet("http-host") {
			httpHost = configuredHost
		}
		if !cmd.IsSet("http-port") {
			httpPort = configuredPort
		}
	}
	if httpHost == mcpHost && httpPort == mcpPort {
		return fmt.Errorf("HTTP and MCP servers cannot listen on the same address %s", net.JoinHostPort(httpHost, httpPort))
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	type serviceResult struct {
		name string
		err  error
	}
	resultCh := make(chan serviceResult, 3)
	wg.Add(3)

	go func() {
		defer wg.Done()
		c := cmdWithBootstrap(bc,
			&cli.StringFlag{Name: "host", Value: httpHost},
			&cli.StringFlag{Name: "port", Value: httpPort},
		)
		log.Println("starting HTTP server")
		resultCh <- serviceResult{name: "HTTP server", err: serveHTTP(ctx, c)}
	}()

	go func() {
		defer wg.Done()
		c := cmdWithBootstrap(bc,
			&cli.StringFlag{Name: "host", Value: mcpHost},
			&cli.StringFlag{Name: "port", Value: mcpPort},
		)
		log.Println("starting MCP HTTP server (streamable http on /mcp, sse on /sse)")
		resultCh <- serviceResult{name: "MCP server", err: mcpServeCmd(ctx, c)}
	}()

	go func() {
		defer wg.Done()
		c := cmdWithBootstrap(bc,
			&cli.DurationFlag{Name: "interval", Value: consolidateInterval},
			&cli.StringSliceFlag{Name: "namespaces", Value: consolidateNamespaces},
		)
		log.Println("starting consolidation service")
		resultCh <- serviceResult{name: "consolidation service", err: consolidateServeCmd(ctx, c)}
	}()

	var firstErr error
	select {
	case <-ctx.Done():
		log.Println("signal received, waiting for services to stop...")
	case result := <-resultCh:
		if ctx.Err() != nil {
			// A signal can race with a service's normal shutdown notification.
			// Treat that path as a clean stop instead of reporting a false crash.
			firstErr = nil
		} else if result.err != nil {
			firstErr = fmt.Errorf("%s stopped: %w", result.name, result.err)
		} else {
			firstErr = fmt.Errorf("%s stopped unexpectedly", result.name)
		}
		cancel()
		log.Printf("%v", firstErr)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Println("timed out waiting for services to stop")
	}

	return firstErr
}

func cmdWithBootstrap(bc *bootstrap.Context, flags ...cli.Flag) *cli.Command {
	return &cli.Command{
		Flags:    flags,
		Metadata: map[string]any{"bootstrapCtx": bc},
	}
}
