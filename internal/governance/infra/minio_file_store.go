package infra

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/donnel666/remail/internal/governance/domain"
	"github.com/minio/minio-go/v7"
)

// MinIOFileStore implements governance FilePort using a private MinIO bucket.
type MinIOFileStore struct {
	client  *minio.Client
	bucket  string
	mu      sync.Mutex
	ensured bool
}

// NewMinIOFileStore creates a private file store backed by MinIO.
func NewMinIOFileStore(client *minio.Client, bucket string) *MinIOFileStore {
	return &MinIOFileStore{client: client, bucket: bucket}
}

// SavePrivate writes a private file and returns safe storage metadata.
func (s *MinIOFileStore) SavePrivate(ctx context.Context, file domain.PrivateFile) (*domain.StoredPrivateFile, error) {
	return s.SavePrivateStream(ctx, domain.PrivateFileStream{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Content:     bytes.NewReader(file.ContentBytes),
		Size:        int64(len(file.ContentBytes)),
	})
}

// SavePrivateStream writes a private file from a stream and returns safe storage metadata.
func (s *MinIOFileStore) SavePrivateStream(ctx context.Context, file domain.PrivateFileStream) (*domain.StoredPrivateFile, error) {
	if file.ObjectKey == "" {
		return nil, fmt.Errorf("private file object key is required")
	}
	if file.ContentType == "" {
		file.ContentType = "application/octet-stream"
	}
	if file.Content == nil {
		return nil, fmt.Errorf("private file content is required")
	}
	if file.Size < 0 {
		return nil, fmt.Errorf("private file size is required")
	}
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}

	_, err := s.client.PutObject(ctx, s.bucket, file.ObjectKey, file.Content, file.Size, minio.PutObjectOptions{
		ContentType: file.ContentType,
		UserMetadata: map[string]string{
			"file-name": file.FileName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("put private file: %w", err)
	}

	return &domain.StoredPrivateFile{
		ObjectKey:   file.ObjectKey,
		FileName:    file.FileName,
		ContentType: file.ContentType,
		Size:        file.Size,
	}, nil
}

// ReadPrivate reads a private file by object key without exposing object storage details.
func (s *MinIOFileStore) ReadPrivate(ctx context.Context, objectKey string) (*domain.PrivateFile, error) {
	if objectKey == "" {
		return nil, fmt.Errorf("private file object key is required")
	}
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}

	object, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get private file: %w", err)
	}
	defer object.Close()

	info, err := object.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat private file: %w", err)
	}
	content, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("read private file: %w", err)
	}

	return &domain.PrivateFile{
		ObjectKey:    objectKey,
		FileName:     info.UserMetadata["file-name"],
		ContentType:  info.ContentType,
		ContentBytes: content,
	}, nil
}

func (s *MinIOFileStore) DeletePrivate(ctx context.Context, objectKey string) error {
	if objectKey == "" {
		return nil
	}
	if err := s.ensureBucket(ctx); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete private file: %w", err)
	}
	return nil
}

func (s *MinIOFileStore) ensureBucket(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ensured {
		return nil
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check private bucket: %w", err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			exists, checkErr := s.client.BucketExists(ctx, s.bucket)
			if checkErr != nil || !exists {
				return fmt.Errorf("create private bucket: %w", err)
			}
		}
	}
	s.ensured = true
	return nil
}
