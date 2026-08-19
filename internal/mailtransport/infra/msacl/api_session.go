package msacl

import "context"

func NewAPISession(ctx context.Context, proxy string, timeoutSeconds int) (*Session, error) {
	return newPlainSession(ctx, proxy, timeoutSeconds)
}

// NewAppleAPISession uses one deterministic Chrome TLS profile. Apple
// workflows provide the matching desktop browser headers at the request layer.
func NewAppleAPISession(ctx context.Context, proxy string, timeoutSeconds int) (*Session, error) {
	return newAppleSession(ctx, proxy, timeoutSeconds)
}

// Request exposes the shared TLS client for protocol clients that need custom
// headers, JSON bodies, or redirect handling.
func (s *Session) Request(method, rawURL string, headers map[string]string, jsonBody any, followRedirects bool) (*HTTPResponse, error) {
	return s.do(method, rawURL, requestOptions{
		Headers:           headers,
		JSON:              jsonBody,
		AllowRedirects:    followRedirects,
		HasAllowRedirects: true,
	})
}

func (s *Session) GetJSON(rawURL string, headers map[string]string, out any) (*HTTPResponse, error) {
	resp, err := s.Get(rawURL, requestOptions{Headers: headers})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := resp.JSON(out); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func (s *Session) PostFormJSON(rawURL string, form map[string]string, headers map[string]string, out any) (*HTTPResponse, error) {
	resp, err := s.Post(rawURL, requestOptions{
		Headers: headers,
		Data:    form,
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := resp.JSON(out); err != nil {
			return resp, err
		}
	}
	return resp, nil
}
