package app

import (
	"strings"
	"testing"
)

func TestParseInboundEmail(t *testing.T) {
	plain := "From: Customer <customer@example.com>\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nHello reply body\r\n"
	body, auto := parseInboundEmail([]byte(plain))
	if auto || strings.TrimSpace(body) != "Hello reply body" {
		t.Fatalf("plain body=%q auto=%v", body, auto)
	}

	boundary := "BOUND123"
	multipart := "From: c@example.com\r\nContent-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n" +
		"--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nplain part\r\n" +
		"--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n<p>html part</p>\r\n" +
		"--" + boundary + "--\r\n"
	body, _ = parseInboundEmail([]byte(multipart))
	if strings.TrimSpace(body) != "plain part" {
		t.Fatalf("multipart body=%q", body)
	}

	autoReply := "From: c@example.com\r\nAuto-Submitted: auto-replied\r\nContent-Type: text/plain\r\n\r\nout of office\r\n"
	if _, auto := parseInboundEmail([]byte(autoReply)); !auto {
		t.Fatal("expected auto-response detection")
	}
}

func TestIsBounceEnvelope(t *testing.T) {
	if !isBounceEnvelope("") || !isBounceEnvelope("<>") || isBounceEnvelope("customer@example.com") {
		t.Fatal("unexpected bounce classification")
	}
}
