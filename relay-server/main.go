// Command relay-server runs the Limoni Voice relay: WebSocket signalling on /ws plus an
// optional low-latency UDP datagram relay on the same port number.
//
// Environment:
//
//	PORT                   TCP port for HTTP/WebSocket (default 27850)
//	UDP_PORT               UDP relay port (default: same as PORT, "0" or "off" disables)
//	RELAY_UDP_PUBLIC_ADDR  host:port advertised to clients for UDP (use when the WebSocket
//	                       hostname does not reach this machine directly, e.g. behind a tunnel)
//	RELAY_AUTH_TOKEN       optional shared secret clients must present; several may be given
//	                       separated by commas (e.g. the old and the new one while rotating)
//	RELAY_AUTH_TOKEN_FILE  file with one accepted secret per line ("#" starts a comment). It is
//	                       read again on SIGHUP and whenever it changes, so secrets rotate
//	                       without a restart; open connections are kept
//	TRUST_PROXY            "1" to always trust CF-Connecting-IP / X-Forwarded-For
//	LOG_FORMAT             "json" (default) or "text"
//	LOG_LEVEL              "debug", "info" (default), "warn", "error"
package main

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
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
	envTokens := strings.Split(authToken, ",")
	tokenFile := strings.TrimSpace(os.Getenv("RELAY_AUTH_TOKEN_FILE"))
	tokens := envTokens
	if tokenFile != "" {
		fileTokens, err := readTokenFile(tokenFile)
		if err != nil {
			logger.Error("cannot read RELAY_AUTH_TOKEN_FILE", "path", tokenFile, "err", err)
			os.Exit(1)
		}
		tokens = append(tokens, fileTokens...)
	}

	udpPort := 0
	udpSetting := strings.ToLower(strings.TrimSpace(envOr("UDP_PORT", port)))
	if udpSetting != "0" && udpSetting != "off" && udpSetting != "false" {
		if p, err := strconv.Atoi(udpSetting); err == nil && p > 0 && p < 65536 {
			udpPort = p
		}
	}

	cfg := relay.Config{
		AuthTokens:    tokens,
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

	if tokenFile != "" {
		go watchTokenFile(server, tokenFile, envTokens, logger)
	}

	if hasToken(tokens) {
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

func hasToken(tokens []string) bool {
	for _, t := range tokens {
		if strings.TrimSpace(t) != "" {
			return true
		}
	}
	return false
}

// readTokenFile returns the secrets listed in path, one per line.
func readTokenFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tokens []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			tokens = append(tokens, line)
		}
	}
	return tokens, sc.Err()
}

// watchTokenFile reloads the token file on SIGHUP and when its modification time changes.
// A file that cannot be read keeps the secrets already in use.
func watchTokenFile(server *relay.Server, path string, envTokens []string, logger *slog.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()

	var lastMod time.Time
	if st, err := os.Stat(path); err == nil {
		lastMod = st.ModTime()
	}
	for {
		forced := false
		select {
		case <-hup:
			forced = true
		case <-tick.C:
		}
		st, err := os.Stat(path)
		if err != nil {
			logger.Warn("token file unavailable, keeping current secrets", "path", path, "err", err)
			continue
		}
		if !forced && st.ModTime().Equal(lastMod) {
			continue
		}
		lastMod = st.ModTime()
		fileTokens, err := readTokenFile(path)
		if err != nil {
			logger.Warn("token file unreadable, keeping current secrets", "path", path, "err", err)
			continue
		}
		tokens := append(append([]string(nil), envTokens...), fileTokens...)
		server.SetAuthTokens(tokens...)
		logger.Info("relay secrets reloaded", "count", len(fileTokens), "auth", hasToken(tokens))
	}
}
