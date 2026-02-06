// Package errors defines custom error types for the Nostr event store.
// All errors are concrete types that can be type-asserted, enabling precise error handling.
package errors

import (
	"fmt"
)

// Error is the base interface for all store errors.
type Error interface {
	error
	// Code returns the error code string (e.g., "ErrEventNotFound", "ErrIndexCorrupted").
	Code() string
	// Unwrap returns the underlying error, supporting error wrapping chain.
	Unwrap() error
}

// baseError is the base implementation of Error.
type baseError struct {
	code    string
	message string
	cause   error
}

func (e *baseError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.code, e.message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.code, e.message)
}

func (e *baseError) Code() string {
	return e.code
}

func (e *baseError) Unwrap() error {
	return e.cause
}

// NewError creates a new error with the given code and message.
func NewError(code, message string) Error {
	return &baseError{
		code:    code,
		message: message,
	}
}

// NewErrorWithCause creates a new error with an underlying cause.
func NewErrorWithCause(code, message string, cause error) Error {
	return &baseError{
		code:    code,
		message: message,
		cause:   cause,
	}
}

// ErrEventNotFound is returned when an event with the given ID is not found in storage.
var ErrEventNotFound = NewError("ErrEventNotFound", "event not found")

// ErrEventAlreadyExists is returned when attempting to insert an event with a duplicate ID.
var ErrEventAlreadyExists = NewError("ErrEventAlreadyExists", "event already exists")

// ErrInvalidEvent is returned when an event fails validation (e.g., invalid signature).
var ErrInvalidEvent = NewError("ErrInvalidEvent", "event validation failed")

// ErrIndexNotFound is returned when an index file is not found.
var ErrIndexNotFound = NewError("ErrIndexNotFound", "index file not found")

// ErrIndexCorrupted is returned when an index file is detected to be corrupted.
var ErrIndexCorrupted = NewError("ErrIndexCorrupted", "index file corrupted")

// ErrTransactionAborted is returned when a transaction is aborted (e.g., due to conflict).
var ErrTransactionAborted = NewError("ErrTransactionAborted", "transaction aborted")

// ErrTimeout is returned when an operation times out.
var ErrTimeout = NewError("ErrTimeout", "operation timeout")

// ErrMemoryExceeded is returned when allocated memory exceeds the configured limit.
var ErrMemoryExceeded = NewError("ErrMemoryExceeded", "memory limit exceeded")

// ErrStorageNotInitialized is returned when store operations are attempted on an uninitialized store.
var ErrStorageNotInitialized = NewError("ErrStorageNotInitialized", "storage not initialized")

// ErrStorageAlreadyInitialized is returned when attempting to initialize an already-initialized store.
var ErrStorageAlreadyInitialized = NewError("ErrStorageAlreadyInitialized", "storage already initialized")

// ErrInvalidPageSize is returned when the configured page size is not supported (must be 4KB, 8KB, or 16KB).
var ErrInvalidPageSize = NewError("ErrInvalidPageSize", "invalid page size")

// ErrRecoveryFailed is returned when crash recovery fails unrecoverably.
var ErrRecoveryFailed = NewError("ErrRecoveryFailed", "crash recovery failed")

// ErrWALCorrupted is returned when the write-ahead log is detected as corrupted.
var ErrWALCorrupted = NewError("ErrWALCorrupted", "WAL corrupted")

// ErrCompactionInProgress is returned when an operation cannot proceed because compaction is running.
var ErrCompactionInProgress = NewError("ErrCompactionInProgress", "compaction in progress")

// NewEventNotFound creates an error for a specific event ID not being found.
func NewEventNotFound(eventID string) Error {
	return NewError("ErrEventNotFound", fmt.Sprintf("event %s not found", eventID))
}

// NewIndexError creates a generic index error with the given message.
func NewIndexError(message string, cause error) Error {
	return NewErrorWithCause("ErrIndexError", message, cause)
}

// NewStorageError creates a generic storage error with the given message.
func NewStorageError(message string, cause error) Error {
	return NewErrorWithCause("ErrStorageError", message, cause)
}

// NewWALError creates a generic WAL error with the given message.
func NewWALError(message string, cause error) Error {
	return NewErrorWithCause("ErrWALError", message, cause)
}

// NewRecoveryError creates a recovery error with the given message.
func NewRecoveryError(message string, cause error) Error {
	return NewErrorWithCause("ErrRecoveryError", message, cause)
}

// NewConfigError creates a configuration error with the given message.
func NewConfigError(message string, cause error) Error {
	return NewErrorWithCause("ErrConfigError", message, cause)
}

// IsEventNotFound checks if the error is an ErrEventNotFound.
func IsEventNotFound(err error) bool {
	e, ok := err.(Error)
	return ok && e.Code() == "ErrEventNotFound"
}

// IsIndexCorrupted checks if the error is an ErrIndexCorrupted.
func IsIndexCorrupted(err error) bool {
	e, ok := err.(Error)
	return ok && e.Code() == "ErrIndexCorrupted"
}

// IsWALCorrupted checks if the error is an ErrWALCorrupted.
func IsWALCorrupted(err error) bool {
	e, ok := err.(Error)
	return ok && e.Code() == "ErrWALCorrupted"
}
