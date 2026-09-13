// Command limoni-sign generates the ed25519 key pair used to sign release checksums
// and signs checksums.txt so the in-app updater can verify releases.
//
//	limoni-sign keygen                       # prints UPDATE_SIGNING_KEY / UPDATE_SIGNING_PUBKEY
//	limoni-sign sign checksums.txt           # reads UPDATE_SIGNING_KEY, writes checksums.txt.sig
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fail(err)
		}
		fmt.Printf("UPDATE_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Printf("UPDATE_SIGNING_PUBKEY=%s\n", base64.StdEncoding.EncodeToString(pub))
	case "sign":
		if len(os.Args) != 3 {
			usage()
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			fail(fmt.Errorf("UPDATE_SIGNING_KEY must be a base64 ed25519 seed"))
		}
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail(err)
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
		if err := os.WriteFile(os.Args[2]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0644); err != nil {
			fail(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: limoni-sign keygen | limoni-sign sign <checksums.txt>")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "limoni-sign:", err)
	os.Exit(1)
}
