package logging

import (
	"bytes"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(Warn, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hidden")
	log.Warn("shown")
	if bytes.Contains(buf.Bytes(), []byte("hidden")) {
		t.Fatal("info leaked at warn level")
	}
	if !bytes.Contains(buf.Bytes(), []byte("shown")) {
		t.Fatal("warn not emitted")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{"debug": Debug, "info": Info, "warn": Warn, "error": Error, "": Info}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Fatalf("ParseLevel(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Fatal("expected error for invalid level")
	}
}
