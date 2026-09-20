package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thebanri/limoni-voice/internal/sysaudio"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

type AppScreen int

const (
	ScreenLobby AppScreen = iota
	ScreenRoom
)

func printUsage() {
	fmt.Println(`🍋 Limoni Voice - Terminal-Native P2P Voice Chat & Screen Sharing

Usage:
  limoni-voice [flags]

Flags:
  --relay <url>         Custom WebSocket relay URL for self-hosted servers
                        Example: --relay ws://192.168.1.100:27850/ws
                        (Set to 'none' or 'off' to disable relay)
  --relay-token <token> Authentication token for password-protected relay servers
  --token <token>       Alias for --relay-token
  --lan, --lan-only     Force LAN-only offline mode (disables relay, direct P2P on local network)
  --offline             Alias for --lan
  --peer, --connect     Direct target peer IP/host for cross-subnet or VPN LAN P2P
                        Example: --peer 192.168.1.50:50000
  --version             Show version information
  --help, -h            Show this help message

Environment Variables:
  LIMONI_RELAY_URL      Override default WebSocket relay URL
  LIMONI_RELAY_TOKEN    Authentication token for protected relay servers
  LIMONI_LAN_ONLY       Set to 1 / true to enable LAN-only mode by default
  LIMONI_OFFLINE        Set to 1 / true to enable offline mode
  LIMONI_PEER           Set direct target peer IP/host
  LIMONI_AUDIO_BACKEND  Set to "exec" to force external audio tools instead of native APIs

Examples:
  # Standard launch (connects to default public relay + LAN auto-discovery):
  limoni-voice

  # Force LAN-only direct P2P communication on local Wi-Fi / Ethernet:
  limoni-voice --lan

  # Connect directly to a specific LAN peer IP:
  limoni-voice --lan --peer 192.168.1.50

  # Connect using your self-hosted Docker relay server:
  limoni-voice --relay ws://192.168.1.100:27850/ws

  # Connect to a password-protected self-hosted relay:
  limoni-voice --relay wss://yourdomain.com/ws --relay-token mysecret123`)
}

func main() {
	var (
		flagRelay      = flag.String("relay", "", "Custom WebSocket relay URL (e.g. ws://192.168.1.100:27850/ws, or 'none' for LAN only)")
		flagRelayToken = flag.String("relay-token", "", "Authentication token for protected relay server (or set LIMONI_RELAY_TOKEN)")
		flagToken      = flag.String("token", "", "Alias for -relay-token")
		flagLAN        = flag.Bool("lan", false, "Force LAN-only offline mode (disables relay connection)")
		flagLANOnly    = flag.Bool("lan-only", false, "Alias for -lan")
		flagOffline    = flag.Bool("offline", false, "Alias for -lan")
		flagPeer       = flag.String("peer", "", "Direct target peer IP / host for LAN / VPN P2P (e.g. 192.168.1.50)")
		flagConnect    = flag.String("connect", "", "Alias for -peer")
		flagHelp       = flag.Bool("help", false, "Show help and usage instructions")
		flagVersion    = flag.Bool("version", false, "Show version information")
		flagSysAudio   = flag.Bool("sysaudio-test", false, "Capture system (desktop) audio for 5 seconds and print the level, then exit")
	)
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stdout)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if *flagHelp {
		printUsage()
		os.Exit(0)
	}

	if *flagSysAudio {
		os.Exit(runSystemAudioTest())
	}

	if *flagVersion {
		fmt.Printf("Limoni Voice %s (Go 1.26+ | E2EE CPace + AES-256-GCM | Opus 48 kHz | P2P Full-Mesh)\n", AppVersion)
		os.Exit(0)
	}

	b := driver.NewBackend(os.Stdin, os.Stdout)
	if err := b.Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing terminal backend: %v\n", err)
		os.Exit(1)
	}
	defer b.Close()

	// Ensure alternate screen starts completely wiped
	_, _ = os.Stdout.WriteString("\x1b[2J\x1b[H")

	t, err := terminal.New(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing terminal: %v\n", err)
		os.Exit(1)
	}

	b.StartEventLoop()

	randNum, _ := rand.Int(rand.Reader, big.NewInt(100000))
	localID := fmt.Sprintf("peer_%d_%d", time.Now().Unix()%10000, randNum.Int64())

	audio := NewAudioEngine()
	node := NewP2PNode(localID, "User", audio)

	// Load persistent configuration from settings.json
	cfg := LoadAppConfig()

	if *flagLAN || *flagLANOnly || *flagOffline {
		node.LanOnly = true
		node.RelayURL = ""
	} else if *flagRelay != "" {
		if strings.EqualFold(*flagRelay, "none") || strings.EqualFold(*flagRelay, "off") {
			node.LanOnly = true
			node.RelayURL = ""
		} else {
			node.RelayURL = NormalizeRelayURL(*flagRelay)
		}
	} else if cfg.RelayURL != "" {
		if strings.EqualFold(cfg.RelayURL, "none") || strings.EqualFold(cfg.RelayURL, "off") {
			node.LanOnly = true
			node.RelayURL = ""
		} else {
			node.RelayURL = NormalizeRelayURL(cfg.RelayURL)
		}
	}

	switch {
	case *flagRelayToken != "":
		node.RelayToken = *flagRelayToken
	case *flagToken != "":
		node.RelayToken = *flagToken
	case cfg.RelayToken != "":
		node.RelayToken = cfg.RelayToken
	}

	if *flagPeer != "" {
		node.SetTargetPeer(*flagPeer)
	} else if *flagConnect != "" {
		node.SetTargetPeer(*flagConnect)
	}

	if err := node.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting P2P node: %v\n", err)
		os.Exit(1)
	}

	NewApp(b, t, node, audio, cfg).Run()
}

// runSystemAudioTest captures what the computer is playing for five seconds and prints the
// level, so screen share audio problems can be diagnosed without starting a call.
func runSystemAudioTest() int {
	fmt.Printf("Limoni Voice %s — system audio capture test (%s)\n", AppVersion, runtime.GOOS)
	fmt.Println("Play some sound (music, a video) on your default output device now.")
	var frames atomic.Int64
	var peak atomic.Int64
	stream, err := sysaudio.Open(func(frame []int16) {
		frames.Add(1)
		var p int16
		for _, s := range frame {
			p = max(p, s, -s)
		}
		if int64(p) > peak.Load() {
			peak.Store(int64(p))
		}
	})
	if err != nil {
		fmt.Println("FAILED:", err)
		if runtime.GOOS == "windows" {
			fmt.Println("Windows captures the default playback device (Sound settings → Output).")
		}
		return 1
	}
	defer stream.Close()
	fmt.Println("Capturing from:", stream.Backend())
	for i := range 5 {
		time.Sleep(time.Second)
		level := float64(peak.Swap(0)) / 32768
		bar := strings.Repeat("█", int(level*40))
		db := "silent"
		if level > 0 {
			db = fmt.Sprintf("%.0f dBFS", 20*math.Log10(level))
		}
		fmt.Printf("  %ds  %-40s %s\n", i+1, bar, db)
	}
	total := frames.Load()
	fmt.Printf("Captured %d frames (%.1f s of audio).\n", total, float64(total)*0.02)
	if total == 0 {
		fmt.Println("RESULT: no audio was captured. Nothing is playing on the default output, or")
		fmt.Println("another application holds it in exclusive mode.")
		return 1
	}
	fmt.Println("RESULT: system audio capture works; screen share can carry it.")
	return 0
}
