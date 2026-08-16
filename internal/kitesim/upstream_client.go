package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type PhoneNumberOffer struct {
	BuyPrice    stringValue `json:"buyPrice"`
	CountryCode string      `json:"countryCode"`
	PhoneCode   stringValue `json:"phoneCode"`
	PhoneNumber string      `json:"phoneNumber"`
}

type NumberPackage struct {
	ID             stringValue     `json:"id"`
	CountryCode    stringValue     `json:"countryCode"`
	PhoneNumber    stringValue     `json:"phoneNumber"`
	DurationType   int             `json:"durationType"`
	DurationValue  int             `json:"durationValue"`
	BuyPrice       stringValue     `json:"buyPrice"`
	OriginalPrice  stringValue     `json:"originalPrice"`
	AutoRenewPrice stringValue     `json:"autoRenewPrice"`
	RawPayload     json.RawMessage `json:"-"`
}

func (p *NumberPackage) UnmarshalJSON(data []byte) error {
	type wire NumberPackage
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = NumberPackage(value)
	p.RawPayload = append(p.RawPayload[:0], data...)
	return nil
}

type createdOrder struct {
	OrderNo string `json:"orderNo"`
}

func (c *Client) Balance(ctx context.Context, token string) (string, error) {
	var response apiResponse[struct {
		Balance stringValue `json:"balance"`
	}]
	if err := c.doJSON(ctx, http.MethodGet, "/user/info", nil, token, nil, &response); err != nil {
		return "", err
	}
	if response.Code != http.StatusOK {
		return "", remoteError(response.Code, response.Message)
	}
	if strings.TrimSpace(string(response.Data.Balance)) == "" {
		return "", errors.New("kitesim: balance missing from upstream response")
	}
	return upstreamDecimal(response.Data.Balance)
}

func (c *Client) PhoneCountries(ctx context.Context, token string) ([]string, error) {
	codes := make([]string, 0)
	seen := map[string]struct{}{}
	for page := 1; page <= 20; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "size": {"100"}}
		var response apiResponse[struct {
			Records []struct {
				TwoWordsCode string     `json:"twoWordsCode"`
				Status       *boolValue `json:"status"`
			} `json:"records"`
			TotalPage int `json:"totalPage"`
		}]
		if err := c.doJSON(ctx, http.MethodGet, "/countryCode/page", query, token, nil, &response); err != nil {
			return nil, err
		}
		if response.Code != http.StatusOK {
			return nil, remoteError(response.Code, response.Message)
		}
		for _, record := range response.Data.Records {
			if record.Status != nil && !bool(*record.Status) {
				continue
			}
			code := strings.ToUpper(strings.TrimSpace(record.TwoWordsCode))
			if code == "" {
				continue
			}
			if _, exists := seen[code]; exists {
				continue
			}
			seen[code] = struct{}{}
			codes = append(codes, code)
		}
		if response.Data.TotalPage > 0 && page >= response.Data.TotalPage || len(response.Data.Records) < 100 {
			break
		}
	}
	sort.Strings(codes)
	return codes, nil
}

func (c *Client) PhoneNumbers(ctx context.Context, token, countryCode string) ([]PhoneNumberOffer, error) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if countryCode == "" {
		return nil, ErrInvalidInput
	}
	var response apiResponse[[]PhoneNumberOffer]
	if err := c.doJSON(ctx, http.MethodGet, "/countryCode/getPhoneNumber/"+url.PathEscape(countryCode), nil, token, nil, &response); err != nil {
		return nil, err
	}
	if response.Code != http.StatusOK {
		return nil, remoteError(response.Code, response.Message)
	}
	return response.Data, nil
}

func (c *Client) NumberPackages(ctx context.Context, token, countryCode, phoneNumber string) ([]NumberPackage, error) {
	query := url.Values{
		"countryCode": {strings.ToUpper(strings.TrimSpace(countryCode))},
		"phoneNumber": {strings.TrimSpace(phoneNumber)},
	}
	var response apiResponse[[]NumberPackage]
	if err := c.doJSON(ctx, http.MethodGet, "/package/getNumberPackageList", query, token, nil, &response); err != nil {
		return nil, err
	}
	if response.Code != http.StatusOK {
		return nil, remoteError(response.Code, response.Message)
	}
	return response.Data, nil
}

func (c *Client) CreatePhoneOrder(ctx context.Context, token, countryCode, phoneNumber, packageID string) (string, error) {
	var response apiResponse[createdOrder]
	err := c.doJSON(ctx, http.MethodPost, "/userPhonePurchase/buyPhoneNumberOrder", nil, token, map[string]any{
		"countryCode": strings.ToUpper(strings.TrimSpace(countryCode)),
		"phoneNumber": strings.TrimSpace(phoneNumber),
		"packageId":   strings.TrimSpace(packageID),
		"couponId":    nil, "autoRenew": 0, "isSelected": 0,
		"couponType": nil, "type": 1, "serviceId": "",
	}, &response)
	if err != nil {
		return "", err
	}
	if response.Code != http.StatusOK {
		return "", remoteError(response.Code, response.Message)
	}
	if strings.TrimSpace(response.Data.OrderNo) == "" {
		return "", errors.New("kitesim: order number missing")
	}
	return strings.TrimSpace(response.Data.OrderNo), nil
}

func (c *Client) ConfirmPhoneOrder(ctx context.Context, token, orderNo string) error {
	var response apiResponse[json.RawMessage]
	if err := c.doJSON(ctx, http.MethodPost, "/userPhonePurchase/confirmPay", nil, token, map[string]any{
		"paymentMethod": 1, "payChannel": "1", "orderNo": strings.TrimSpace(orderNo),
	}, &response); err != nil {
		return err
	}
	if response.Code != http.StatusOK {
		return remoteError(response.Code, response.Message)
	}
	return nil
}

func (c *Client) PhoneOrderDetail(ctx context.Context, token, orderNo string) (*PhoneOrder, error) {
	query := url.Values{"orderNo": {strings.TrimSpace(orderNo)}}
	var response apiResponse[PhoneOrder]
	if err := c.doJSON(ctx, http.MethodGet, "/userPhonePurchase/getOrderDetail", query, token, nil, &response); err != nil {
		return nil, err
	}
	if response.Code != http.StatusOK || strings.TrimSpace(string(response.Data.ID)) == "" {
		return nil, remoteError(response.Code, response.Message)
	}
	return &response.Data, nil
}

func (c *Client) CreateRenewalOrder(ctx context.Context, token string, phone PhoneOrder, packageID string) (string, error) {
	var response apiResponse[json.RawMessage]
	err := c.doJSON(ctx, http.MethodPost, "/userPhoneRenewal/createRenewalNumberOrder", nil, token, map[string]any{
		"autoRenew": nil, "serviceId": nil, "type": nil,
		"phoneNumber": strings.TrimSpace(phone.PhoneNumber),
		"packageId":   strings.TrimSpace(packageID), "isSelected": 0,
		"countryCode": strings.ToUpper(strings.TrimSpace(string(phone.CountryCode))),
		"couponId":    nil, "couponType": nil,
	}, &response)
	if err != nil {
		return "", err
	}
	if response.Code != http.StatusOK {
		return "", remoteError(response.Code, response.Message)
	}
	orderNo, err := orderNoFromJSON(response.Data)
	if err != nil {
		return "", err
	}
	return orderNo, nil
}

func (c *Client) ConfirmRenewalOrder(ctx context.Context, token, orderNo string) error {
	var response apiResponse[json.RawMessage]
	if err := c.doJSON(ctx, http.MethodPost, "/userPhoneRenewal/confirmPayRenew", nil, token, map[string]any{
		"paymentMethod": 1, "payChannel": "1", "orderNo": strings.TrimSpace(orderNo), "transactionId": "",
	}, &response); err != nil {
		return err
	}
	if response.Code != http.StatusOK {
		return remoteError(response.Code, response.Message)
	}
	return nil
}

func orderNoFromJSON(raw json.RawMessage) (string, error) {
	var object createdOrder
	if json.Unmarshal(raw, &object) == nil && strings.TrimSpace(object.OrderNo) != "" {
		return strings.TrimSpace(object.OrderNo), nil
	}
	var value string
	if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}
	return "", errors.New("kitesim: order number missing")
}

const topNumberSegmentScore = 10000

func pickBestNumber(numbers []PhoneNumberOffer, localCounts map[string]int) (PhoneNumberOffer, error) {
	if len(numbers) == 0 {
		return PhoneNumberOffer{}, errors.New("kitesim: no phone numbers available")
	}
	remoteCounts := make(map[string]int, len(numbers))
	for _, number := range numbers {
		remoteCounts[numberSegment(number)]++
	}
	segments := make([]string, 0, len(remoteCounts))
	for segment := range remoteCounts {
		segments = append(segments, segment)
	}
	sort.Slice(segments, func(i, j int) bool {
		if remoteCounts[segments[i]] != remoteCounts[segments[j]] {
			return remoteCounts[segments[i]] < remoteCounts[segments[j]]
		}
		return segments[i] < segments[j]
	})

	bestSegment := segments[0]
	bestScore := topNumberSegmentScore - localCounts[bestSegment]
	for rank, segment := range segments[1:] {
		score := topNumberSegmentScore - rank - 1 - localCounts[segment]
		if score > bestScore {
			bestSegment, bestScore = segment, score
		}
	}

	var best PhoneNumberOffer
	found := false
	for _, candidate := range numbers {
		if numberSegment(candidate) != bestSegment {
			continue
		}
		if !found || decimalLess(candidate.BuyPrice, best.BuyPrice) {
			best, found = candidate, true
		}
	}
	return best, nil
}

func numberSegment(number PhoneNumberOffer) string {
	phone := strings.TrimSpace(number.PhoneNumber)
	code := strings.TrimPrefix(strings.TrimSpace(string(number.PhoneCode)), "+")
	phone = strings.TrimPrefix(phone, code)
	if len(phone) > 3 {
		return phone[:3]
	}
	return phone
}

func decimalLess(left, right stringValue) bool {
	l, lerr := decimal.NewFromString(strings.TrimSpace(string(left)))
	r, rerr := decimal.NewFromString(strings.TrimSpace(string(right)))
	return lerr == nil && (rerr != nil || l.LessThan(r))
}
