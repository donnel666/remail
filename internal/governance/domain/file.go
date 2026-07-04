package domain

import "io"

// PrivateFile is a file that must stay in the private object bucket.
type PrivateFile struct {
	ObjectKey    string
	FileName     string
	ContentType  string
	ContentBytes []byte
}

// PrivateFileStream is used when callers can provide content without keeping
// the whole file in memory.
type PrivateFileStream struct {
	ObjectKey   string
	FileName    string
	ContentType string
	Content     io.Reader
	Size        int64
}

// StoredPrivateFile is the safe metadata returned after a private file is stored.
type StoredPrivateFile struct {
	ObjectKey   string
	FileName    string
	ContentType string
	Size        int64
}
