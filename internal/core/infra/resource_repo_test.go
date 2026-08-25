package infra

import (
	"reflect"
	"testing"
)

func TestUniqueMicrosoftEmails(t *testing.T) {
	got := uniqueMicrosoftEmails([]string{
		" User@Example.com ",
		"user@example.com",
		"",
		"second@example.com",
		" SECOND@example.com ",
	})
	want := []string{"second@example.com", "User@Example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueMicrosoftEmails mismatch:\nwant %#v\ngot  %#v", want, got)
	}
}

func TestMicrosoftEmailDomainCanonicalizesTrailingDot(t *testing.T) {
	if got := microsoftEmailDomain("User@Outlook.com."); got != "outlook.com" {
		t.Fatalf("microsoftEmailDomain() = %q, want outlook.com", got)
	}
}
