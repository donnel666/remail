package proton

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ProtonMail/gopenpgp/v3/crypto"
)

const messagePageSize = 150

type apiMessage struct {
	ID, AddressID, Subject, MIMEType, Header, ExternalID string
	Body                                                 *string
	Sender                                               Address
	ToList, CCList, BCCList                              []Address
	Time                                                 int64
}

func (c *Client) Fetch(ctx context.Context, session *Session, req FetchRequest) (FetchResult, error) {
	result := FetchResult{Messages: []Message{}}
	req.Recipient = strings.ToLower(strings.TrimSpace(req.Recipient))
	if session == nil || session.Version != 1 || session.UID == "" || session.AccessToken == "" {
		return result, &Failure{Category: "session_revoked", SafeMessage: "No reusable Proto session is available."}
	}
	var address *AddressKeys
	for i := range session.Addresses {
		if strings.EqualFold(session.Addresses[i].Email, req.Recipient) && len(session.Addresses[i].PrivateKeys) > 0 {
			address = &session.Addresses[i]
			break
		}
	}
	if req.Recipient == "" || address == nil {
		return result, &Failure{Category: "identity_mismatch", SafeMessage: "The Proto session does not contain the requested receiving address."}
	}
	if req.UntilAt.IsZero() {
		req.UntilAt = time.Now().UTC()
	}
	if !req.SinceAt.IsZero() && req.SinceAt.After(req.UntilAt) {
		return result, &Failure{Category: "protocol", SafeMessage: "The Proto mail time range is invalid."}
	}
	keys, err := privateKeyRing(address.PrivateKeys)
	if err != nil {
		return result, err
	}
	defer keys.ClearPrivateParams()
	t, err := c.transport(req.ProxyURL)
	if err != nil {
		return result, err
	}
	defer t.client.CloseIdleConnections()
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(time.Now().UTC()) {
		if err := t.refreshForFetch(ctx, session, req); err != nil {
			return result, err
		}
	}
	known := make(map[string]bool, len(req.KnownMessageIDs))
	if !req.FullHistory {
		for _, id := range req.KnownMessageIDs {
			known[strings.ToLower(strings.TrimSpace(id))] = true
		}
	}
	limit := req.MaxMessages
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 1000)
	type candidate struct {
		message apiMessage
		folder  string
	}
	var candidates []candidate
	seen := make(map[string]bool)
	complete := true
	emit := func(messages []Message) error {
		if len(messages) == 0 {
			return nil
		}
		if req.OnMessages != nil {
			return req.OnMessages(messages)
		}
		result.Messages = append(result.Messages, messages...)
		return nil
	}
	for _, folder := range []struct{ label, name string }{{"0", "Inbox"}, {"4", "Junk"}} {
		folderCount := 0
		pageIDs := make(map[string]bool)
		for page := 0; ; page++ {
			query := url.Values{
				"Page": {strconv.Itoa(page)}, "PageSize": {strconv.Itoa(messagePageSize)},
				"LabelID": {folder.label}, "Sort": {"Time"}, "Desc": {"1"},
				"AddressID": {address.ID}, "End": {strconv.FormatInt(req.UntilAt.Unix(), 10)},
			}
			if !req.SinceAt.IsZero() {
				query.Set("Begin", strconv.FormatInt(req.SinceAt.Unix(), 10))
			}
			var response struct {
				Messages []apiMessage
				Total    *int
				Stale    json.RawMessage
			}
			if err := t.authenticated(ctx, session, "/mail/v4/messages?"+query.Encode(), &response, req); err != nil {
				return result, err
			}
			if response.Messages == nil || flagEnabled(response.Stale) {
				return result, &Failure{Category: "protocol", SafeMessage: "Proto mail pagination changed or returned no message list.", Retryable: true}
			}
			var batch []Message
			knownBoundary := false
			for _, meta := range response.Messages {
				if meta.ID == "" || meta.Time <= 0 || pageIDs[meta.ID] {
					return result, &Failure{Category: "protocol", SafeMessage: "Proto mail pagination returned an invalid or repeated message.", Retryable: true}
				}
				pageIDs[meta.ID] = true
				receivedAt := time.Unix(meta.Time, 0).UTC()
				if receivedAt.Before(req.SinceAt) || receivedAt.After(req.UntilAt) || !messageForAddress(meta, *address) || seen[meta.ID] {
					continue
				}
				if messageKnown(meta, folder.name, known) {
					knownBoundary, complete = true, false
					break
				}
				seen[meta.ID] = true
				folderCount++
				if !req.FullHistory {
					candidates = append(candidates, candidate{message: meta, folder: folder.name})
					if folderCount >= limit {
						break
					}
					continue
				}
				message, err := t.readMessage(ctx, session, meta.ID, folder.name, *address, keys, req)
				if err != nil {
					return result, err
				}
				batch = append(batch, message)
			}
			if err := emit(batch); err != nil {
				return result, err
			}
			if knownBoundary || (!req.FullHistory && folderCount >= limit) {
				complete = false
				break
			}
			if len(response.Messages) < messagePageSize {
				if response.Total != nil && len(pageIDs) < *response.Total {
					return result, &Failure{Category: "protocol", SafeMessage: "Proto mail pagination ended before all messages were read.", Retryable: true}
				}
				break
			}
		}
	}
	if !req.FullHistory {
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].message.Time != candidates[j].message.Time {
				return candidates[i].message.Time > candidates[j].message.Time
			}
			return candidates[i].message.ID < candidates[j].message.ID
		})
		if len(candidates) > limit {
			candidates, complete = candidates[:limit], false
		}
		var batch []Message
		for _, candidate := range candidates {
			message, err := t.readMessage(ctx, session, candidate.message.ID, candidate.folder, *address, keys, req)
			if err != nil {
				return result, err
			}
			batch = append(batch, message)
		}
		if err := emit(batch); err != nil {
			return result, err
		}
	}
	result.Complete = complete
	return result, nil
}

func messageForAddress(message apiMessage, address AddressKeys) bool {
	hasRecipient := false
	for _, list := range [][]Address{message.ToList, message.CCList, message.BCCList} {
		for _, recipient := range list {
			hasRecipient = hasRecipient || strings.TrimSpace(recipient.Address) != ""
			if strings.EqualFold(strings.TrimSpace(recipient.Address), address.Email) {
				return true
			}
		}
	}
	return !hasRecipient && address.ID != "" && message.AddressID == address.ID
}

func messageKnown(message apiMessage, folder string, known map[string]bool) bool {
	id := strings.ToLower(strings.TrimSpace(message.ID))
	external := strings.ToLower(strings.Trim(strings.TrimSpace(message.ExternalID), "<>"))
	return known[id] || (external != "" && known["internet:"+external]) ||
		known["provider:proton:"+strings.ToLower(folder)+":"+id]
}

func (t *transport) readMessage(ctx context.Context, session *Session, id, folder string, address AddressKeys, keys *crypto.KeyRing, request FetchRequest) (Message, error) {
	var response struct{ Message *apiMessage }
	if err := t.authenticated(ctx, session, "/mail/v4/messages/"+url.PathEscape(id), &response, request); err != nil {
		return Message{}, err
	}
	message := response.Message
	if message == nil || message.Body == nil {
		return Message{}, &Failure{Category: "protocol", SafeMessage: "Proto returned an incomplete encrypted message."}
	}
	if message.ID != id || message.Time <= 0 || !messageForAddress(*message, address) {
		return Message{}, &Failure{Category: "protocol", SafeMessage: "Proto returned a message outside the requested mailbox."}
	}
	decrypted, err := decryptPGP(*message.Body, keys)
	if err != nil {
		return Message{}, err
	}
	body, err := readableBody(decrypted, message.MIMEType)
	clear(decrypted)
	if err != nil {
		return Message{}, err
	}
	return Message{ID: message.ID, AddressID: message.AddressID, Subject: message.Subject,
		Body: body, MIMEType: message.MIMEType, Header: message.Header, ExternalID: message.ExternalID,
		Folder: folder, Sender: message.Sender, ToList: message.ToList, CCList: message.CCList,
		BCCList: message.BCCList, OriginalToCount: len(message.ToList), ReceivedAt: time.Unix(message.Time, 0).UTC()}, nil
}
