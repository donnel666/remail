package mailbox

import (
	"bytes"
	"encoding/base64"
	htmlcharset "golang.org/x/net/html/charset"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"strings"
)

func ReadMIMEBody(contentType string, transferEncoding string, body io.Reader) (string, error) {
	value, _, _, err := ReadMIMEBodyPart(contentType, transferEncoding, body)
	return value, err
}

func ReadMIMEBodyPart(contentType string, transferEncoding string, body io.Reader) (string, bool, bool, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		var htmlBody, plainBody string
		var hasHTML, hasPlain bool
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", false, false, err
			}
			if disposition, _, dispositionErr := mime.ParseMediaType(part.Header.Get("Content-Disposition")); dispositionErr == nil && strings.EqualFold(disposition, "attachment") {
				continue
			}
			partBody, partIsHTML, found, err := ReadMIMEBodyPart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil || !found {
				continue
			}
			if partIsHTML {
				if !hasHTML {
					htmlBody, hasHTML = partBody, true
				}
			} else if !hasPlain {
				plainBody, hasPlain = partBody, true
			}
		}
		if hasHTML {
			return htmlBody, true, true, nil
		}
		if hasPlain {
			return plainBody, false, true, nil
		}
		return "", false, false, nil
	}
	if !strings.EqualFold(mediaType, "text/plain") && !strings.EqualFold(mediaType, "text/html") {
		return "", false, false, nil
	}

	reader := decodeTransferReader(body, transferEncoding)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", false, false, err
	}
	value := decodeMIMEText(data, params["charset"])
	return value, mediaType == "text/html", value != "", nil
}

func decodeMIMEText(data []byte, label string) string {
	if label = strings.TrimSpace(label); label != "" {
		if reader, err := htmlcharset.NewReaderLabel(label, bytes.NewReader(data)); err == nil {
			if decoded, err := io.ReadAll(reader); err == nil {
				data = decoded
			}
		}
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func decodeTransferReader(body io.Reader, transferEncoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}
