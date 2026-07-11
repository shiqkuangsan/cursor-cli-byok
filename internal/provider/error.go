package provider

import "fmt"

type Error struct {
	Code       string
	StatusCode int
	Retryable  bool
	cause      error
}

func (err *Error) Error() string {
	if err == nil {
		return "provider request failed"
	}
	if err.StatusCode > 0 {
		return fmt.Sprintf("provider request failed: %s (HTTP %d)", err.Code, err.StatusCode)
	}
	return fmt.Sprintf("provider request failed: %s", err.Code)
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func NewError(code string, statusCode int, retryable bool, cause error) *Error {
	return &Error{Code: code, StatusCode: statusCode, Retryable: retryable, cause: cause}
}
