package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

func TestUploadRuntimeLimitsClampUnsafeValues(t *testing.T) {
	runtimeconfig.Set("resource_import_max_bytes", "2147483647")
	runtimeconfig.Set("max_project_logo_bytes", "2147483647")
	defer runtimeconfig.Delete("resource_import_max_bytes")
	defer runtimeconfig.Delete("max_project_logo_bytes")

	if maxImportBytesValue() != maxConfiguredImportBytes || maxProjectLogoBytesValue() != maxConfiguredProjectLogoBytes {
		t.Fatal("upload limits were not clamped")
	}
}

func TestMultipartLimitAllowsAFileAtTheConfiguredMaximum(t *testing.T) {
	const fileMax int64 = 1024
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "resource.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), int(fileMax))); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("longLived", "false"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if int64(body.Len()) <= fileMax {
		t.Fatal("multipart body should include form overhead")
	}

	request := httptest.NewRequest(http.MethodPost, "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Body = http.MaxBytesReader(httptest.NewRecorder(), request.Body, multipartRequestMaxBytes(fileMax))
	if err := request.ParseMultipartForm(fileMax); err != nil {
		t.Fatalf("parse multipart form: %v", err)
	}
	t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	file, _, err := request.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, fileMax+1))
	if err != nil || int64(len(content)) != fileMax {
		t.Fatalf("read file at configured limit: size=%d err=%v", len(content), err)
	}
}
