// Command limoni-sign generates the ed25519 key pair used to sign release checksums
// and signs checksums.txt so the in-app updater can verify releases.
//
//	limoni-sign keygen                       # prints UPDATE_SIGNING_KEY / UPDATE_SIGNING_PUBKEY
//	limoni-sign sign checksums.txt           # reads UPDATE_SIGNING_KEY, writes checksums.txt.sig
//	limoni-sign verify checksums.txt         # checks checksums.txt.sig against UPDATE_SIGNING_PUBKEY
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
	case "verify":
		if len(os.Args) != 3 {
			usage()
		}
		pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_PUBKEY")))
		if err != nil || len(pub) != ed25519.PublicKeySize {
			fail(fmt.Errorf("UPDATE_SIGNING_PUBKEY must be a base64 ed25519 public key"))
		}
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail(err)
		}
		raw, err := os.ReadFile(os.Args[2] + ".sig")
		if err != nil {
			fail(err)
		}
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
			fail(fmt.Errorf("%s.sig does not verify with UPDATE_SIGNING_PUBKEY: the key pair does not match", os.Args[2]))
		}
		fmt.Println("signature OK")
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: limoni-sign keygen | limoni-sign sign <checksums.txt> | limoni-sign verify <checksums.txt>")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "limoni-sign:", err)
	os.Exit(1)
}
