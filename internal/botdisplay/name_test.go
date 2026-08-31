package botdisplay

import "testing"

func TestNameNeverExposesUserID(t *testing.T) {
	if got := Name(" Alice ", 42); got != "Alice" {
		t.Fatalf("nickname = %q", got)
	}
	if got := Name("", 42); got != "匿名用户" {
		t.Fatalf("anonymous name = %q", got)
	}
	for _, unsafe := range []string{"victim@example.com", "42", "line\nbreak"} {
		if got := Name(unsafe, 42); got != "匿名用户" {
			t.Fatalf("unsafe nickname %q = %q", unsafe, got)
		}
	}
}
