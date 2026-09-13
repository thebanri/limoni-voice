// Command relay-server runs the Limoni Voice relay: WebSocket signalling on /ws plus an
// optional low-latency UDP datagram relay on the same port number.
//
// Environment:
//
//	PORT                   TCP port for HTTP/WebSocket (default 27850)
//	UDP_PORT               UDP relay port (default: same as PORT, "0" or "off" disables)
//	RELAY_UDP_PUBLIC_ADDR  host:port advertised to clients for UDP (use when the WebSocket
//	                       hostname does not reach this machine directly, e.g. behind a tunnel)
//	RELAY_AUTH_TOKEN       optional shared secret clients must present
//	TRUST_PROXY            "1" to always trust CF-Connecting-IP / X-Forwarded-For
//	LOG_FORMAT             "json" (default) or "text"
//	LOG_LEVEL              "debug", "info" (default), "warn", "error"
package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thebanri/limoni-voice/internal/relay"
)

func main() {
	logger := newLogger()
	slog.SetDefault(logger)

	port := envOr("PORT", "27850")
	authToken := os.Getenv("RELAY_AUTH_TOKEN")
	if authToken == "" {
		authToken = os.Getenv("LIMONI_AUTH_TOKEN")
	}

	udpPort := 0
	udpSetting := strings.ToLower(strings.TrimSpace(envOr("UDP_PORT", port)))
	if udpSetting != "0" && udpSetting != "off" && udpSetting != "false" {
		if p, err := strconv.Atoi(udpSetting); err == nil && p > 0 && p < 65536 {
			udpPort = p
		}
	}

	cfg := relay.Config{
		AuthToken:     authToken,
		UDPPort:       udpPort,
		UDPPublicAddr: strings.TrimSpace(os.Getenv("RELAY_UDP_PUBLIC_ADDR")),
		TrustProxy:    os.Getenv("TRUST_PROXY") == "1" || strings.EqualFold(os.Getenv("TRUST_PROXY"), "true"),
		Logger:        logger,
	}

	server := relay.New(cfg)

	if udpPort > 0 {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: udpPort})
		if err != nil {
			logger.Error("UDP relay disabled: listen failed", "port", udpPort, "err", err)
			cfg.UDPPort = 0
			server = relay.New(cfg)
		} else {
			go func() {
				if err := server.ServeUDP(conn); err != nil {
					logger.Error("UDP relay stopped", "err", err)
				}
			}()
			logger.Info("UDP relay listening", "port", udpPort, "advertised", cfg.UDPPublicAddr)
		}
	}

	if cfg.AuthToken != "" {
		logger.Info("relay authentication active")
	} else {
		logger.Warn("relay authentication disabled (public mode, set RELAY_AUTH_TOKEN to restrict)")
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("Limoni Voice relay starting", "port", port)
	if err := httpServer.ListenAndServe(); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
