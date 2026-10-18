package protocol

import (
	"context"
	"github.com/donnel666/remail/internal/mailbox"
	"time"
)

type Credentials struct{ Email, AppPassword, Protocol, ProxyURL string }
type FetchRequest struct {
	Page            *mailbox.PageRequest
	Recipient       string
	SinceAt         time.Time
	UntilAt         time.Time
	MaxMessages     int
	FullHistory     bool
	KnownMessageIDs []string
	OnMessages      func([]Message) error
	OnReset         func()
}
type FetchResult struct {
	Page     *mailbox.PageResult
	Messages []Message
	Complete bool
}
type Address struct{ Name, Address string }
type Message struct {
	ID, AddressID, Subject, Body, MIMEType, Header, ExternalID, Folder, RawHash string
	Sender                                                                      Address
	ToList, CCList, BCCList                                                     []Address
	OriginalToCount                                                             int
	ReceivedAt                                                                  time.Time
}
type Failure struct {
	Category, SafeMessage string
	Stage                 string
	Retryable             bool
	Cause                 error
}

func (e *Failure) Error() string { return e.SafeMessage }
func (e *Failure) Unwrap() error { return e.Cause }

type MailboxClient interface {
	Validate(context.Context, Credentials) error
	Fetch(context.Context, Credentials, FetchRequest) (FetchResult, error)
}
