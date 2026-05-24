// Package domain — typed sentinel errors used across all layers.
// Callers use errors.Is / errors.As to distinguish error classes without
// importing any adapter package.
package domain

import (
	"errors"
	"fmt"
)

// ErrNotFound is the sentinel for "resource does not exist".
// Wrap it with NotFoundError to carry resource context.
var ErrNotFound = errors.New("not found")

// NotFoundError wraps ErrNotFound and carries the resource type + ID so
// the HTTP handler can return a meaningful 404 body without string-matching.
type NotFoundError struct {
	Resource string
	ID       string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %s: not found", e.Resource, e.ID)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// NewNotFoundError is a convenience constructor.
func NewNotFoundError(resource, id string) *NotFoundError {
	return &NotFoundError{Resource: resource, ID: id}
}


