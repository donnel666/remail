package msacl

import "context"

func NewAPISession(ctx context.Context, proxy string, timeoutSeconds int) (*Session, error) {
	return newPlainSession(ctx, proxy, timeoutSeconds)
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
