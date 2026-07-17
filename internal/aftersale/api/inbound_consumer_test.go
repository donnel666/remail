package api

import (
	"strings"
	"testing"
)

func TestInboundConsumerHandles(t *testing.T) {
	consumer := NewInboundConsumer(nil, "support")
	cases := map[string]bool{
		"support+as1-tok@tickets.example.com": true,
		"SUPPORT+as1-tok@tickets.example.com": true,
		"support@tickets.example.com":         false, // no plus tag
		"other+as1-tok@tickets.example.com":   false,
		"not-an-address":                      false,
	}
	for recipient, want := range cases {
		if got := consumer.Handles(recipient); got != want {
			t.Errorf("Handles(%q) = %v, want %v", recipient, got, want)
		}
	}
}

func TestParseInboundEmailPlain(t *testing.T) {
	raw := "From: Customer <customer@example.com>\r\n" +
		"Subject: Re: ticket\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		"Hello reply body\r\n"
	from, name, body, auto := parseInboundEmail([]byte(raw))
	if from != "customer@example.com" || name != "Customer" || auto {
		t.Fatalf("from=%q name=%q auto=%v", from, name, auto)
	}
	if strings.TrimSpace(body) != "Hello reply body" {
		t.Fatalf("body=%q", body)
	}
}

func TestParseInboundEmailMultipartPrefersPlain(t *testing.T) {
	boundary := "BOUND123"
	raw := "From: c@example.com\r\n" +
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"plain part\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		"<p>html part</p>\r\n" +
		"--" + boundary + "--\r\n"
	_, _, body, _ := parseInboundEmail([]byte(raw))
	if strings.TrimSpace(body) != "plain part" {
		t.Fatalf("body=%q", body)
	}
}

func TestParseInboundEmailQuotedPrintable(t *testing.T) {
	raw := "From: c@example.com\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"caf=C3=A9 works\r\n"
	_, _, body, _ := parseInboundEmail([]byte(raw))
	if !strings.Contains(body, "café") {
		t.Fatalf("qp decode body=%q", body)
	}
}

func TestParseInboundEmailHTMLOnly(t *testing.T) {
	raw := "From: c@example.com\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		"<div>hello<br>world</div>\r\n"
	_, _, body, _ := parseInboundEmail([]byte(raw))
	if !strings.Contains(body, "hello") || !strings.Contains(body, "world") {
		t.Fatalf("html body=%q", body)
	}
}

func TestParseInboundEmailAutoResponse(t *testing.T) {
	raw := "From: c@example.com\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"out of office\r\n"
	if _, _, _, auto := parseInboundEmail([]byte(raw)); !auto {
		t.Fatal("expected auto-response detection")
	}
}

func TestIsBounceEnvelope(t *testing.T) {
	if !isBounceEnvelope("") || !isBounceEnvelope("<>") {
		t.Fatal("empty and <> envelopes are bounces")
	}
	if isBounceEnvelope("customer@example.com") {
		t.Fatal("real sender is not a bounce")
	}
}
