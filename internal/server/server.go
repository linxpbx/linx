// Package server runs HTTP servers with safe timeouts and graceful shutdown.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

// New returns a server for h on addr with Linx's timeouts. Set TLSConfig
// (with GetCertificate) to serve HTTPS.
func New(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// TLSConfig is the HTTPS settings every Linx web server uses: TLS 1.2 or
// newer, the certificate from getCert (reloaded on renewal).
func TLSConfig(getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: getCert}
}

// Run serves h on addr until SIGINT/SIGTERM, then drains for up to 10 s.
func Run(addr string, h http.Handler, log *slog.Logger) error {
	return Serve(log, New(addr, h))
}

// Serve runs every server until SIGINT/SIGTERM or until one of them fails,
// then shuts them all down, draining for up to 10 s.
func Serve(log *slog.Logger, srvs ...*http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, len(srvs))
	for _, srv := range srvs {
		go func() {
			if srv.TLSConfig != nil {
				log.Info("listening", "addr", srv.Addr, "tls", true)
				errCh <- srv.ListenAndServeTLS("", "")
				return
			}
			log.Info("listening", "addr", srv.Addr)
			errCh <- srv.ListenAndServe()
		}()
	}

	var runErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range srvs {
		if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}
