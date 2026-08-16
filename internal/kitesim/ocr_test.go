package kitesim

import (
	"os"
	"testing"
)

func TestSolveCaptcha(t *testing.T) {
	image, err := os.ReadFile("testdata/NUY7.png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := solveCaptcha(image)
	if err != nil {
		t.Fatal(err)
	}
	if got != "NUY7" {
		t.Fatalf("solveCaptcha() = %q, want NUY7", got)
	}
}
