// Command daum checks one mailbox over IMAP or POP3 without platform state.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

const maxCredentialBytes = 64 << 10

type options struct {
	File, Protocol, ProxyEnv string
	Limit                    int
	Timeout                  time.Duration
}

type credential struct {
	Mailbox, LoginPassword, AppPassword string
}

type mailSummary struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	Subject    string    `json:"subject"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type result struct {
	Success      bool          `json:"success"`
	Mailbox      string        `json:"mailbox,omitempty"`
	Protocol     string        `json:"protocol,omitempty"`
	Network      string        `json:"network,omitempty"`
	Elapsed      string        `json:"elapsed"`
	MessageCount int           `json:"messageCount"`
	Messages     []mailSummary `json:"messages"`
	Category     string        `json:"category,omitempty"`
	Message      string        `json:"message,omitempty"`
}

type mailboxFetcher interface {
	Fetch(context.Context, protocol.Credentials, protocol.FetchRequest) (protocol.FetchResult, error)
}

var codePattern = regexp.MustCompile(`\b[0-9]{4,8}\b`)
var urlPattern = regexp.MustCompile(`https?://[^\s<>]+`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, protocol.NewClient())
	stop()
	os.Exit(code)
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("daum", flag.ContinueOnError)
	// Malformed flags may contain pasted credentials; never print their values.
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.File, "file", "", "single-account TXT file; otherwise read one line from stdin")
	flags.StringVar(&opts.Protocol, "protocol", "imap", "imap or pop3")
	flags.StringVar(&opts.ProxyEnv, "proxy-env", "", "environment variable containing an HTTP/HTTPS/SOCKS5 proxy URL; omitted means direct diagnosis")
	flags.IntVar(&opts.Limit, "limit", 3, "maximum message summaries (1-100)")
	flags.DurationVar(&opts.Timeout, "timeout", 45*time.Second, "mailbox operation timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stderr)
			flags.PrintDefaults()
			return opts, flag.ErrHelp
		}
		return opts, errors.New("invalid command flags; use -help; credentials must be supplied through stdin or -file")
	}
	opts.Protocol = strings.ToLower(strings.TrimSpace(opts.Protocol))
	if flags.NArg() != 0 || (opts.Protocol != "imap" && opts.Protocol != "pop3") || opts.Limit < 1 || opts.Limit > 100 || opts.Timeout <= 0 || (opts.ProxyEnv != "" && !environmentName.MatchString(opts.ProxyEnv)) {
		return opts, errors.New("use -protocol imap|pop3, -limit 1-100, a positive -timeout, and no positional arguments")
	}
	return opts, nil
}

func parseCredential(line string) (credential, error) {
	invalid := errors.New("expected email----APP password, email----login password----APP password, or five fields including pickup URL and Kakao email")
	line = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), "\ufeff")
	if len(line) > maxCredentialBytes || !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n\x00") {
		return credential{}, invalid
	}
	parts := strings.Split(line, "----")
	if len(parts) != 2 && len(parts) != 3 && len(parts) != 5 {
		return credential{}, invalid
	}
	value := credential{Mailbox: strings.ToLower(strings.TrimSpace(parts[0]))}
	address, err := mail.ParseAddress(value.Mailbox)
	_, suffix, found := strings.Cut(value.Mailbox, "@")
	if err != nil || address.Address != value.Mailbox || !found || utf8.RuneCountInString(value.Mailbox) > 255 || (suffix != "daum.net" && suffix != "hanmail.net" && suffix != "hanmail2.net") {
		return credential{}, invalid
	}
	if len(parts) == 2 {
		value.AppPassword = parts[1]
	} else {
		value.LoginPassword, value.AppPassword = parts[1], parts[2]
	}
	if value.AppPassword == "" || utf8.RuneCountInString(value.AppPassword) > 512 {
		return credential{}, invalid
	}
	// The optional pickup link and Kakao account are independent metadata, not protocol credentials.
	return value, nil
}

func readCredential(opts options, stdin io.Reader) (credential, error) {
	input := stdin
	if opts.File != "" {
		file, err := os.Open(opts.File)
		if err != nil {
			return credential{}, errors.New("credential file could not be opened")
		}
		defer file.Close()
		input = file
	}
	reader := bufio.NewReader(io.LimitReader(input, maxCredentialBytes+1))
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return credential{}, errors.New("credential input could not be read")
	}
	if line == "" {
		return credential{}, errors.New("no credential line was provided")
	}
	if opts.File != "" {
		rest, err := io.ReadAll(reader)
		if err != nil || strings.TrimSpace(string(rest)) != "" {
			return credential{}, errors.New("credential file must contain one account line")
		}
	}
	return parseCredential(line)
}

func run(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, client mailboxFetcher) int {
	started := time.Now()
	out := result{Messages: []mailSummary{}}
	finish := func(code int) int {
		out.Elapsed = time.Since(started).Round(time.Millisecond).String()
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			fmt.Fprintln(stderr, "daum: result output failed")
			return 1
		}
		return code
	}
	opts, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		out.Category, out.Message = "input", err.Error()
		return finish(2)
	}
	out.Protocol = opts.Protocol
	out.Network = "direct"
	proxyURL := ""
	if opts.ProxyEnv != "" {
		proxyURL, err = proxydomain.NormalizeProxyURL(os.Getenv(opts.ProxyEnv))
		if err != nil || proxyURL == "" {
			out.Category, out.Message = "input", "Selected proxy environment variable must contain a valid proxy URL."
			return finish(2)
		}
		out.Network = "proxy"
	}
	account, err := readCredential(opts, stdin)
	if err != nil {
		out.Category, out.Message = "input", err.Error()
		return finish(2)
	}
	out.Mailbox = account.Mailbox
	ctx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()
	fetched, err := client.Fetch(ctx, protocol.Credentials{Email: account.Mailbox, AppPassword: account.AppPassword, Protocol: opts.Protocol, ProxyURL: proxyURL}, protocol.FetchRequest{MaxMessages: opts.Limit})
	if err != nil {
		out.Category, out.Message = "provider", "Daum mailbox check failed."
		var failure *protocol.Failure
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			out.Category, out.Message = "timeout", "Daum mailbox check timed out."
		case errors.Is(err, context.Canceled):
			out.Category, out.Message = "canceled", "Daum mailbox check was canceled."
		case errors.As(err, &failure):
			switch failure.Category {
			case "invalid_credentials", "protocol_disabled", "rate_limited", "provider_unavailable", "transport", "protocol", "message_too_large", "mime":
				out.Category = failure.Category
			}
		}
		return finish(1)
	}
	for _, message := range fetched.Messages[:min(len(fetched.Messages), opts.Limit)] {
		out.Messages = append(out.Messages, mailSummary{ID: safeSummary(message.ID, account), From: safeSummary(message.Sender.Address, account), Subject: safeSummary(message.Subject, account), ReceivedAt: message.ReceivedAt})
	}
	out.Success, out.MessageCount = true, len(out.Messages)
	return finish(0)
}

func safeSummary(value string, account credential) string {
	for _, secret := range []string{account.LoginPassword, account.AppPassword} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = urlPattern.ReplaceAllString(value, "[URL]")
	return codePattern.ReplaceAllString(value, "[code]")
}
