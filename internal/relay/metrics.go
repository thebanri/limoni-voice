package relay

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// Metrics are exported in Prometheus text format on /metrics.
type Metrics struct {
	connections       atomic.Int64
	roomsCreated      atomic.Uint64
	joinRequests      atomic.Uint64
	admitted          atomic.Uint64
	rejected          atomic.Uint64
	pakeForwarded     atomic.Uint64
	hostMigrations    atomic.Uint64
	hijackBlocked     atomic.Uint64
	authFailures      atomic.Uint64
	rateLimited       atomic.Uint64
	wsBytesIn         atomic.Uint64
	wsBytesOut        atomic.Uint64
	wsForwarded       atomic.Uint64
	udpForwarded      atomic.Uint64
	udpPacketsIn      atomic.Uint64
	udpPacketsOut     atomic.Uint64
	udpBytesIn        atomic.Uint64
	udpBytesOut       atomic.Uint64
	udpUnknownToken   atomic.Uint64
	framesDropped     atomic.Uint64
	targetedForwarded atomic.Uint64
	kicked            atomic.Uint64
	banned            atomic.Uint64
	roomsExpired      atomic.Uint64
}

func newMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) serveHTTP(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		rooms, members, pending, idle := len(s.rooms), 0, 0, 0
		var idleMax time.Duration
		now := time.Now()
		for _, rm := range s.rooms {
			members += len(rm.members)
			pending += len(rm.pending)
			if d := rm.idleFor(now); d >= idleRoomAfter {
				idle++
				idleMax = max(idleMax, d)
			}
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		gauge := func(name, help string, v int64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
		}
		counter := func(name, help string, v uint64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
		}
		gauge("limoni_relay_connections", "Open WebSocket connections.", m.connections.Load())
		gauge("limoni_relay_rooms", "Active rooms.", int64(rooms))
		gauge("limoni_relay_members", "Admitted room members (including reconnect grace).", int64(members))
		gauge("limoni_relay_pending_joins", "Joiners waiting for host approval.", int64(pending))
		gauge("limoni_relay_rooms_idle", "Rooms without relayed traffic or signalling for 5 minutes (direct-path rooms are idle on the relay).", int64(idle))
		gauge("limoni_relay_room_idle_max_seconds", "Longest time an idle room has gone without relay activity.", int64(idleMax.Seconds()))
		counter("limoni_relay_rooms_created_total", "Rooms created.", m.roomsCreated.Load())
		counter("limoni_relay_join_requests_total", "Join requests forwarded to hosts.", m.joinRequests.Load())
		counter("limoni_relay_admitted_total", "Joiners admitted by hosts.", m.admitted.Load())
		counter("limoni_relay_rejected_total", "Joiners rejected by hosts.", m.rejected.Load())
		counter("limoni_relay_pake_forwarded_total", "Handshake frames forwarded.", m.pakeForwarded.Load())
		counter("limoni_relay_rooms_expired_total", "Empty rooms closed after the reconnect grace period.", m.roomsExpired.Load())
		counter("limoni_relay_kicked_total", "Members removed by their host.", m.kicked.Load())
		counter("limoni_relay_banned_total", "Members removed and banned by their host.", m.banned.Load())
		counter("limoni_relay_host_migrations_total", "Host migrations.", m.hostMigrations.Load())
		counter("limoni_relay_hijack_blocked_total", "Rejected room or member identity takeovers.", m.hijackBlocked.Load())
		counter("limoni_relay_auth_failures_total", "Connections rejected for a missing or invalid relay token.", m.authFailures.Load())
		counter("limoni_relay_rate_limited_total", "Requests rejected by per-IP limits.", m.rateLimited.Load())
		counter("limoni_relay_ws_bytes_in_total", "WebSocket bytes received.", m.wsBytesIn.Load())
		counter("limoni_relay_ws_bytes_out_total", "WebSocket bytes sent.", m.wsBytesOut.Load())
		counter("limoni_relay_ws_frames_forwarded_total", "Encrypted frames received over WebSocket and forwarded.", m.wsForwarded.Load())
		counter("limoni_relay_udp_frames_forwarded_total", "Encrypted datagrams received over UDP and forwarded.", m.udpForwarded.Load())
		counter("limoni_relay_udp_packets_in_total", "UDP datagrams received.", m.udpPacketsIn.Load())
		counter("limoni_relay_udp_packets_out_total", "UDP datagrams sent.", m.udpPacketsOut.Load())
		counter("limoni_relay_udp_bytes_in_total", "UDP bytes received.", m.udpBytesIn.Load())
		counter("limoni_relay_udp_bytes_out_total", "UDP bytes sent.", m.udpBytesOut.Load())
		counter("limoni_relay_udp_unknown_token_total", "UDP datagrams with an unknown token.", m.udpUnknownToken.Load())
		counter("limoni_relay_frames_dropped_total", "Frames dropped because a receiver queue was full.", m.framesDropped.Load())
		counter("limoni_relay_targeted_forwarded_total", "Frames forwarded to a single member (screen share).", m.targetedForwarded.Load())
	}
}
