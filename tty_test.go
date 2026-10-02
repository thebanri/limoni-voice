package main

import (
	"os"
	"testing"
)

func TestPipesAndTheNullDeviceAreNotTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	for name, f := range map[string]*os.File{"pipe reader": r, "pipe writer": w, os.DevNull: null} {
		if isTerminal(f) {
			t.Errorf("isTerminal(%s) = true", name)
		}
	}
}
