package proton

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/emersion/go-message"
	// Register the installed MIME charset decoders for decrypted message bodies.
	_ "github.com/emersion/go-message/charset"
)

func unlockPrivateKey(armored string, password []byte) (string, error) {
	key, err := crypto.NewPrivateKeyFromArmored(armored, password)
	if err != nil {
		return "", err
	}
	defer key.ClearPrivateParams()
	if unlocked, err := key.IsUnlocked(); err != nil || !unlocked {
		return "", errors.New("unusable private key")
	}
	return key.Armor()
}

func privateKeyRing(keys []string) (*crypto.KeyRing, error) {
	ring, err := crypto.NewKeyRing(nil)
	if err != nil {
		return nil, err
	}
	for _, armored := range keys {
		key, err := crypto.NewKeyFromArmored(armored)
		if err != nil {
			ring.ClearPrivateParams()
			return nil, &Failure{Category: "action_required", SafeMessage: "Stored Proto keys could not be read."}
		}
		if err := ring.AddKey(key); err != nil {
			key.ClearPrivateParams()
			ring.ClearPrivateParams()
			return nil, &Failure{Category: "action_required", SafeMessage: "Stored Proto keys are unusable."}
		}
	}
	return ring, nil
}

func unlockAddressKey(key apiKey, userKeys *crypto.KeyRing, passwords map[string][]byte) (string, error) {
	if key.Token == "" {
		for _, password := range passwords {
			if unlocked, err := unlockPrivateKey(key.PrivateKey, password); err == nil {
				return unlocked, nil
			}
		}
		return "", errors.New("address key cannot be unlocked")
	}
	if key.Signature == "" {
		return "", errors.New("address key token signature is missing")
	}
	token, err := decryptPGP(key.Token, userKeys)
	if err != nil {
		return "", err
	}
	defer clear(token)
	verifier, err := crypto.PGP().Verify().VerificationKeys(userKeys).New()
	if err != nil {
		return "", err
	}
	verified, err := verifier.VerifyDetached(token, []byte(key.Signature), crypto.Armor)
	if err != nil {
		return "", err
	}
	if err := verified.SignatureError(); err != nil {
		return "", err
	}
	return unlockPrivateKey(key.PrivateKey, token)
}

func decryptPGP(body string, keys *crypto.KeyRing) ([]byte, error) {
	decryptor, err := crypto.PGP().Decryption().DecryptionKeys(keys).MaxDecompressedMessageSize(maxMessageBytes).New()
	if err != nil {
		return nil, &Failure{Category: "decryption", SafeMessage: "Proto decryption keys are unavailable."}
	}
	plain, err := decryptor.Decrypt([]byte(body), crypto.Armor)
	if err != nil {
		return nil, &Failure{Category: "decryption", SafeMessage: "A Proto message could not be decrypted."}
	}
	return plain.Bytes(), nil
}

func readableBody(body []byte, mimeType string) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(mimeType)
	if !strings.HasPrefix(mediaType, "multipart/") && mediaType != "message/rfc822" {
		return string(body), nil
	}
	entity, err := message.Read(bytes.NewReader(body))
	if err != nil {
		return "", &Failure{Category: "decryption", SafeMessage: "The decrypted Proto MIME message could not be parsed."}
	}
	var plain, html []string
	if err := collectMIMEBody(entity, 0, &plain, &html); err != nil {
		return "", &Failure{Category: "decryption", SafeMessage: "The decrypted Proto MIME body is incomplete or unsupported."}
	}
	if len(html) > 0 {
		return strings.Join(html, "\n"), nil
	}
	return strings.Join(plain, "\n"), nil
}

func collectMIMEBody(entity *message.Entity, depth int, plain, html *[]string) error {
	if depth > 20 {
		return errors.New("MIME nesting limit")
	}
	mediaType, params, err := entity.Header.ContentType()
	if err != nil {
		return err
	}
	disposition, file, _ := entity.Header.ContentDisposition()
	// Skip the entire attachment subtree, including nested multipart attachments.
	if disposition == "attachment" || file["filename"] != "" || params["name"] != "" {
		return nil
	}
	if mediaType == "message/rfc822" {
		nested, err := message.Read(entity.Body)
		if err != nil {
			return err
		}
		return collectMIMEBody(nested, depth+1, plain, html)
	}
	if reader := entity.MultipartReader(); reader != nil {
		defer reader.Close()
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := collectMIMEBody(part, depth+1, plain, html); err != nil {
				return err
			}
		}
	}
	if mediaType != "text/plain" && mediaType != "text/html" {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(entity.Body, maxMessageBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return errors.New("MIME body size limit")
	}
	if mediaType == "text/html" {
		*html = append(*html, string(data))
	} else {
		*plain = append(*plain, string(data))
	}
	return nil
}
