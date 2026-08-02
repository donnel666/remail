package gmail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestSMSBowerClientParsesCatalogActivationAndThreeCodeActions(t *testing.T) {
	statuses := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "secret", r.URL.Query().Get("api_key"))
		switch r.URL.Path {
		case "/stubs/handler_api.php":
			switch r.URL.Query().Get("action") {
			case "getBalance":
				_, _ = w.Write([]byte("ACCESS_BALANCE:12.50"))
			case "getMailServicesList":
				_, _ = w.Write([]byte(`{"status":"success","services":[{"code":"go","name":"Google"}]}`))
			}
		case "/api/mail/getPriceRests":
			_, _ = w.Write([]byte(`{"status":1,"data":{"go":{"gmail.com":{"price":1.25,"count":7}}}}`))
		case "/api/mail/getActivation":
			require.Equal(t, "gmail.com", r.URL.Query().Get("domain"))
			require.Equal(t, "0", r.URL.Query().Get("alias"))
			_, _ = w.Write([]byte(`{"status":1,"mail":"demo@gmail.com","mailId":41}`))
		case "/api/mail/getCode":
			_, _ = w.Write([]byte(`{"status":1,"code":"123456"}`))
		case "/api/mail/setStatus":
			statuses = append(statuses, r.URL.Query().Get("status"))
			_, _ = w.Write([]byte(`{"status":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newSMSBowerClient(server.URL, server.Client())

	balance, err := client.Balance(context.Background(), "secret")
	require.NoError(t, err)
	require.True(t, balance.Equal(decimal.RequireFromString("12.5")))
	services, err := client.Services(context.Background(), "secret")
	require.NoError(t, err)
	require.Equal(t, []SMSBowerService{{Code: "go", Name: "Google"}}, services)
	prices, err := client.GmailPrices(context.Background(), "secret")
	require.NoError(t, err)
	require.Equal(t, 7, prices["go"].Count)
	activation, err := client.Activate(context.Background(), "secret", "go", decimal.RequireFromString("1.25"))
	require.NoError(t, err)
	require.Equal(t, uint64(41), activation.MailID)
	code, err := client.Code(context.Background(), "secret", activation.MailID)
	require.NoError(t, err)
	require.Equal(t, "123456", code)
	for _, status := range []int{5, 5, 3} {
		require.NoError(t, client.SetStatus(context.Background(), "secret", activation.MailID, status))
	}
	require.Equal(t, []string{"5", "5", "3"}, statuses)
}

func TestSMSBowerActivationTransportFailureIsUncertainAndWaitingIsSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mail/getCode" {
			_, _ = w.Write([]byte("Code has not been received yet, please try again later"))
			return
		}
		panic(http.ErrAbortHandler)
	}))
	client := newSMSBowerClient(server.URL, server.Client())
	_, err := client.Code(context.Background(), "secret", 1)
	require.ErrorIs(t, err, ErrCodeWaiting)
	_, err = client.Activate(context.Background(), "secret", "go", decimal.NewFromInt(1))
	var uncertain *UncertainActivationError
	require.True(t, errors.As(err, &uncertain))
	server.Close()
}

func TestSMSBowerClientClassifiesNoAccessJSONAsBadKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":0,"message":"No access","data":[]}`))
	}))
	defer server.Close()

	_, err := newSMSBowerClient(server.URL, server.Client()).Balance(context.Background(), "invalid")
	require.ErrorIs(t, err, ErrBadKey)
}

func TestSMSBowerClientClassifiesTerminalActivationErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		call func(*SMSBowerClient) error
		want error
	}{
		{
			name: "missing activation", body: "No activation found with such id", want: ErrActivationMissing,
			call: func(client *SMSBowerClient) error {
				_, err := client.Code(context.Background(), "secret", 1)
				return err
			},
		},
		{
			name: "invalid mail id", body: `{"status":0,"error":"Pass mail id"}`, want: ErrActivationMissing,
			call: func(client *SMSBowerClient) error {
				_, err := client.Code(context.Background(), "secret", 1)
				return err
			},
		},
		{
			name: "terminal status", body: "Bad actual activation status", want: ErrActivationStatus,
			call: func(client *SMSBowerClient) error {
				return client.SetStatus(context.Background(), "secret", 1, 3)
			},
		},
		{
			name: "already cancelled", body: "Activation is already canceled", want: ErrActivationStatus,
			call: func(client *SMSBowerClient) error {
				_, err := client.Code(context.Background(), "secret", 1)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			require.ErrorIs(t, test.call(newSMSBowerClient(server.URL, server.Client())), test.want)
		})
	}
}
