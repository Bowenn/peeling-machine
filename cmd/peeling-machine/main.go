// peeling-machine is a local HTTP/HTTPS debugging proxy.
//
// Usage:
//
//	peeling-machine [flags]          # run proxy + API
//	peeling-machine ca export        # print root CA cert path
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bowen/peeling-machine/internal/api"
	"github.com/bowen/peeling-machine/internal/ca"
	"github.com/bowen/peeling-machine/internal/capture"
	"github.com/bowen/peeling-machine/internal/config"
	"github.com/bowen/peeling-machine/internal/proxy"
	"github.com/bowen/peeling-machine/internal/transport"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "ca" {
		return runCA(args[1:])
	}

	cfg, err := config.Default()
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("peeling-machine", flag.ExitOnError)
	fs.StringVar(&cfg.ProxyAddr, "proxy-addr", cfg.ProxyAddr, "proxy listen address")
	fs.StringVar(&cfg.APIAddr, "api-addr", cfg.APIAddr, "API + SSE listen address")
	fs.StringVar(&cfg.CADir, "ca-dir", cfg.CADir, "directory for root CA material")
	fs.IntVar(&cfg.BufferSize, "buffer-size", cfg.BufferSize, "in-memory capture ring size")
	fs.Int64Var(&cfg.BodyCap, "body-cap", cfg.BodyCap, "per-body byte cap for captures")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rootCA, err := ca.LoadOrCreate(cfg.CADir)
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}
	store := capture.NewStore(cfg.BufferSize)
	proxySrv := proxy.New(cfg.ProxyAddr, rootCA, store, transport.Direct(), cfg.BodyCap, logger)
	apiSrv := api.New(cfg.APIAddr, store, rootCA)

	logger.Info("peeling-machine starting",
		"proxy", cfg.ProxyAddr, "api", cfg.APIAddr, "ca", rootCA.CertPath())

	errCh := make(chan error, 2)
	go func() {
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("proxy: %w", err)
		}
	}()
	go func() {
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("api: %w", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-sigCh:
		logger.Info("shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = proxySrv.Shutdown(ctx)
	_ = apiSrv.Shutdown(ctx)
	return nil
}

func runCA(args []string) error {
	cfg, err := config.Default()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("peeling-machine ca", flag.ExitOnError)
	fs.StringVar(&cfg.CADir, "ca-dir", cfg.CADir, "directory for root CA material")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: peeling-machine ca export")
		os.Exit(2)
	}
	sub := args[0]
	_ = fs.Parse(args[1:])

	switch sub {
	case "export":
		rootCA, err := ca.LoadOrCreate(cfg.CADir)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "WARN: installing this CA lets peeling-machine decrypt TLS from anything that trusts it. Install only on devices you own, and uninstall when finished.")
		fmt.Println(rootCA.CertPath())
		return nil
	default:
		return fmt.Errorf("unknown ca subcommand %q", sub)
	}
}
