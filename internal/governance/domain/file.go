package domain

// PrivateFile is a file that must stay in the private object bucket.
type PrivateFile struct {
	ObjectKey    string
	FileName     string
	ContentType  string
	ContentBytes []byte
}

// StoredPrivateFile is the safe metadata returned after a private file is stored.
type StoredPrivateFile struct {
	ObjectKey   string
	FileName    string
	ContentType string
	Size        int64
}
