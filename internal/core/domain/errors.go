package domain

import "errors"

// Sentinel domain errors for Core (resource) module.
var (
	ErrResourceNotFound        = errors.New("resource not found")
	ErrInvalidResourceType     = errors.New("invalid resource type")
	ErrInvalidResourceStatus   = errors.New("invalid resource status")
	ErrInvalidResourceFilter   = errors.New("invalid resource filter")
	ErrResourceNotPrivate      = errors.New("resource is not private")
	ErrForbiddenResource       = errors.New("forbidden resource access")
	ErrForbiddenPurpose        = errors.New("forbidden resource purpose")
	ErrDuplicateEmail          = errors.New("duplicate email address in resource import")
	ErrDuplicateDomain         = errors.New("domain already exists")
	ErrInvalidImportFormat     = errors.New("invalid import format")
	ErrFileStorageUnavailable  = errors.New("file storage is temporarily unavailable")
	ErrImportQueueUnavailable  = errors.New("resource import queue is temporarily unavailable")
	ErrMailServerNotFound      = errors.New("mail server not found")
	ErrForbiddenMailServer     = errors.New("forbidden mail server access")
	ErrInvalidMailServerStatus = errors.New("invalid mail server status")
	ErrMailServerOwnerMismatch = errors.New("mail server owner does not match resource owner")
	ErrInvalidPurpose          = errors.New("invalid purpose, must be not_sale, sale or binding")
	ErrDomainNotFound          = errors.New("domain not found")
	ErrInvalidDomain           = errors.New("invalid domain")
)
