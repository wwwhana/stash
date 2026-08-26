package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alash3al/stash/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
)

var (
	buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stash_build_info",
			Help: "Build information, value is always 1",
		},
		[]string{"version"},
	)
)

func init() {
	prometheus.MustRegister(buildInfo)
	buildInfo.WithLabelValues("0.2.8").Set(1)
}

func serveHTTP(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)

	host := cmd.String("host")
	port := cmd.String("port")
	if bc != nil && bc.Config != nil {
		configuredHost, configuredPort := configuredHTTPAddress(bc.Config.HTTPAddr, host, port)
		if !cmd.IsSet("host") {
			host = configuredHost
		}
		if !cmd.IsSet("port") {
			port = configuredPort
		}
	}
	addr := net.JoinHostPort(host, port)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := bc.Brain.Health(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := bc.Brain.Ready(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	srv := &http.Server{Addr: addr, Handler: observability.InstrumentHTTP(mux)}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("metrics server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			cancel()
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// configuredHTTPAddress turns STASH_HTTP_ADDR into host/port flags while
// preserving an explicitly supplied command-line value. It accepts the usual
// :9090, 127.0.0.1:9090, and 9090 forms.
func configuredHTTPAddress(raw, fallbackHost, fallbackPort string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallbackHost, fallbackPort
	}
	if strings.HasPrefix(raw, ":") && len(raw) > 1 {
		return fallbackHost, raw[1:]
	}
	if host, port, err := net.SplitHostPort(raw); err == nil && port != "" {
		if host == "" {
			host = fallbackHost
		}
		return host, port
	}
	if !strings.Contains(raw, ":") {
		return fallbackHost, raw
	}
	return fallbackHost, fallbackPort
}
