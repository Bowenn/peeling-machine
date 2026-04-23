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
	"net/url"
	"os"
	"os/signal"
	"strings"
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
	fs.StringVar(&cfg.UpstreamHTTP, "upstream-http", cfg.UpstreamHTTP, "chain through an HTTP upstream proxy, e.g. http://user:pass@host:8080")
	fs.StringVar(&cfg.UpstreamSOCKS5, "upstream-socks5", cfg.UpstreamSOCKS5, "chain through a SOCKS5 proxy, e.g. host:1080 or socks5://user:pass@host:1080")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rootCA, err := ca.LoadOrCreate(cfg.CADir)
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}

	upstream, upstreamKind, err := buildUpstream(cfg, logger)
	if err != nil {
		return fmt.Errorf("upstream: %w", err)
	}

	store := capture.NewStore(cfg.BufferSize)
	proxySrv := proxy.New(cfg.ProxyAddr, rootCA, store, upstream, cfg.BodyCap, logger)
	apiSrv := api.New(cfg.APIAddr, store, rootCA)

	logger.Info("peeling-machine starting",
		"proxy", cfg.ProxyAddr, "api", cfg.APIAddr, "ca", rootCA.CertPath(), "upstream", upstreamKind)

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

// buildUpstream picks the RoundTripper based on flags. If both --upstream-http
// and --upstream-socks5 are set, HTTP wins with a warning (matches plan.md).
func buildUpstream(cfg config.Config, logger *slog.Logger) (transport.RoundTripper, string, error) {
	httpURL := strings.TrimSpace(cfg.UpstreamHTTP)
	socks5Addr := strings.TrimSpace(cfg.UpstreamSOCKS5)
	if httpURL != "" && socks5Addr != "" {
		logger.Warn("both --upstream-http and --upstream-socks5 set; using --upstream-http")
		socks5Addr = ""
	}
	switch {
	case httpURL != "":
		rt, err := transport.HTTP(httpURL)
		if err != nil {
			return nil, "", err
		}
		return rt, "http:" + redactUpstream(httpURL), nil
	case socks5Addr != "":
		rt, err := transport.SOCKS5(socks5Addr)
		if err != nil {
			return nil, "", err
		}
		return rt, "socks5:" + redactUpstream(socks5Addr), nil
	default:
		return transport.Direct(), "direct", nil
	}
}

// redactUpstream strips userinfo (credentials) from a proxy URL before it hits
// logs. Bare host:port inputs are returned unchanged.
func redactUpstream(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.User = nil
	return u.String()
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
