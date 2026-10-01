package main

import (
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on the default mux, served only below
	"os"
)

// startProfiler serves Go's profiler on LIMONI_PPROF (e.g. 127.0.0.1:6060) so a slow or busy
// session can be looked into: go tool pprof http://127.0.0.1:6060/debug/pprof/profile.
// Only a loopback address is accepted; the profiler shows what the program holds.
func startProfiler() {
	addr := os.Getenv("LIMONI_PPROF")
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return
	}
	go func() { _ = http.ListenAndServe(addr, nil) }()
}
