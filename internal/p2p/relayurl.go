package p2p

import "strings"

// NormalizeRelayURL converts user-entered URL, web link, or raw domain into a valid WebSocket relay URL.
// If empty, "default", or "reset", it returns DefaultRelayURL (official relay).
// Explicit LAN keywords ("none", "off", "lan", "local") return "" (LAN Mode).
// Examples:
//   - "" -> "wss://relay.thebanri.dpdns.org/ws"
//   - "default" -> "wss://relay.thebanri.dpdns.org/ws"
//   - "none" / "off" / "lan" -> ""
//   - "voice.thebanri.dpdns.org" -> "wss://voice.thebanri.dpdns.org/ws"
//   - "https://voice.thebanri.dpdns.org" -> "wss://voice.thebanri.dpdns.org/ws"
//   - "http://192.168.1.3:27850" -> "ws://192.168.1.3:27850/ws"
//   - "192.168.1.3:27850" -> "ws://192.168.1.3:27850/ws"
//   - "localhost:27850" -> "ws://localhost:27850/ws"
func NormalizeRelayURL(raw string) string {
	u := strings.TrimSpace(raw)
	if strings.EqualFold(u, "none") || strings.EqualFold(u, "off") || strings.EqualFold(u, "lan") || strings.EqualFold(u, "local") {
		return ""
	}
	if u == "" || strings.EqualFold(u, "default") || strings.EqualFold(u, "reset") {
		return DefaultRelayURL
	}

	hasWss := strings.HasPrefix(strings.ToLower(u), "wss://")
	hasWs := strings.HasPrefix(strings.ToLower(u), "ws://")
	hasHttps := strings.HasPrefix(strings.ToLower(u), "https://")
	hasHttp := strings.HasPrefix(strings.ToLower(u), "http://")

	var scheme string
	var rest string

	if hasWss {
		scheme = "wss://"
		rest = u[6:]
	} else if hasWs {
		scheme = "ws://"
		rest = u[5:]
	} else if hasHttps {
		scheme = "wss://"
		rest = u[8:]
	} else if hasHttp {
		scheme = "ws://"
		rest = u[7:]
	} else {
		// No protocol scheme provided.
		// Determine whether it's local network (ws://) or public domain with SSL (wss://).
		cleanHost := u
		if slashIdx := strings.Index(cleanHost, "/"); slashIdx != -1 {
			cleanHost = cleanHost[:slashIdx]
		}
		hostOnly := cleanHost
		if colonIdx := strings.Index(hostOnly, ":"); colonIdx != -1 {
			hostOnly = hostOnly[:colonIdx]
		}

		isLocal := hostOnly == "localhost" ||
			hostOnly == "127.0.0.1" ||
			strings.HasPrefix(hostOnly, "192.168.") ||
			strings.HasPrefix(hostOnly, "10.") ||
			strings.HasPrefix(hostOnly, "172.")

		if isLocal {
			scheme = "ws://"
		} else {
			scheme = "wss://"
		}
		rest = u
	}

	// Preserve query parameters if present (e.g. ?token=abc)
	query := ""
	if qIdx := strings.Index(rest, "?"); qIdx != -1 {
		query = rest[qIdx:]
		rest = rest[:qIdx]
	}

	// Trim trailing slashes
	rest = strings.TrimRight(rest, "/")

	// If no path is provided or ends without /ws, append /ws
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		rest = rest + "/ws"
	} else {
		pathPart := rest[slashIdx:]
		if pathPart == "" {
			rest = rest + "/ws"
		}
	}

	return scheme + rest + query
}
