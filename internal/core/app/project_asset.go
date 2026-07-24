package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
)

const (
	projectLogoMaxBytes = 2 * 1024 * 1024
	projectLogoPrefix   = "projects/logos/"
)

type ProjectLogo struct {
	ContentType string
	Content     []byte
}

type ProjectAssetUseCase struct {
	files governanceapp.FilePort
}

func NewProjectAssetUseCase(files governanceapp.FilePort) *ProjectAssetUseCase {
	return &ProjectAssetUseCase{files: files}
}

func (uc *ProjectAssetUseCase) SaveLogo(ctx context.Context, fileName string, content []byte) (string, error) {
	if uc.files == nil {
		return "", domain.ErrFileStorageUnavailable
	}
	if len(content) == 0 || len(content) > projectLogoMaxBytes {
		return "", domain.ErrInvalidProject
	}
	contentType, extension, ok := projectLogoContentType(content, fileName)
	if !ok {
		return "", domain.ErrInvalidProject
	}
	objectKey := projectLogoPrefix + platform.NewUUIDV7String() + extension
	stored, err := uc.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    objectKey,
		FileName:     cleanProjectLogoFileName(fileName, extension),
		ContentType:  contentType,
		ContentBytes: content,
	})
	if err != nil {
		return "", domain.ErrFileStorageUnavailable
	}
	return projectLogoURL(stored.ObjectKey), nil
}

func (uc *ProjectAssetUseCase) ReadLogo(ctx context.Context, encodedKey string) (*ProjectLogo, error) {
	if uc.files == nil {
		return nil, domain.ErrFileStorageUnavailable
	}
	objectKeyBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil {
		return nil, domain.ErrProjectNotFound
	}
	objectKey := string(objectKeyBytes)
	if !strings.HasPrefix(objectKey, projectLogoPrefix) {
		return nil, domain.ErrProjectNotFound
	}
	file, err := uc.files.ReadPrivate(ctx, objectKey)
	if err != nil {
		return nil, domain.ErrProjectNotFound
	}
	if file == nil || len(file.ContentBytes) == 0 {
		return nil, domain.ErrProjectNotFound
	}
	return &ProjectLogo{
		ContentType: file.ContentType,
		Content:     file.ContentBytes,
	}, nil
}

func projectLogoURL(objectKey string) string {
	return "/v1/projects/logos/" + base64.RawURLEncoding.EncodeToString([]byte(objectKey))
}

func projectLogoContentType(content []byte, fileName string) (string, string, bool) {
	detected := http.DetectContentType(content)
	switch detected {
	case "image/png":
		return detected, ".png", true
	case "image/jpeg":
		return detected, ".jpg", true
	case "image/gif":
		return detected, ".gif", true
	case "image/webp":
		return detected, ".webp", true
	}
	if validProjectLogoSVG(content) {
		return "image/svg+xml", ".svg", true
	}
	extension := strings.ToLower(filepath.Ext(fileName))
	if extension == ".webp" {
		return "image/webp", ".webp", true
	}
	return "", "", false
}

func validProjectLogoSVG(content []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	seenSVG, depth, closed := false, 0, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return seenSVG && closed
		}
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenSVG || token.Name.Local != "svg" {
					return false
				}
				seenSVG = true
			}
			// Logo uploads stay static; reject active SVG content.
			switch strings.ToLower(token.Name.Local) {
			case "script", "foreignobject", "iframe", "object", "embed":
				return false
			}
			for _, attr := range token.Attr {
				name := strings.ToLower(attr.Name.Local)
				value := strings.ToLower(strings.TrimSpace(attr.Value))
				if strings.HasPrefix(name, "on") || ((name == "href" || name == "src") && strings.HasPrefix(value, "javascript:")) {
					return false
				}
			}
			depth++
		case xml.EndElement:
			if depth == 0 {
				return false
			}
			depth--
			if depth == 0 {
				closed = true
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return false
			}
		}
	}
}

func cleanProjectLogoFileName(fileName string, extension string) string {
	base := strings.TrimSpace(filepath.Base(fileName))
	if base == "." || base == "/" || base == "" {
		return "project-logo" + extension
	}
	return base
}
