package app

import (
	"context"
	"encoding/base64"
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
	extension := strings.ToLower(filepath.Ext(fileName))
	if extension == ".webp" {
		return "image/webp", ".webp", true
	}
	return "", "", false
}

func cleanProjectLogoFileName(fileName string, extension string) string {
	base := strings.TrimSpace(filepath.Base(fileName))
	if base == "." || base == "/" || base == "" {
		return "project-logo" + extension
	}
	return base
}
