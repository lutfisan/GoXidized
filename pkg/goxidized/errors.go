package goxidized

import (
	"errors"
	"fmt"
)

type FailureCategory string

const (
	FailureConnect            FailureCategory = "failed_connect"
	FailureAuth               FailureCategory = "failed_auth"
	FailurePrivilege          FailureCategory = "failed_privilege"
	FailureCommand            FailureCategory = "failed_command"
	FailureTimeout            FailureCategory = "failed_timeout"
	FailureNormalization      FailureCategory = "failed_normalization"
	FailureRedaction          FailureCategory = "failed_redaction"
	FailureStorage            FailureCategory = "failed_storage"
	FailureCredentialProvider FailureCategory = "failed_credential_provider"
)

type BackupError struct {
	Category FailureCategory
	Op       string
	Err      error
}

func (e *BackupError) Error() string {
	if e == nil {
		return ""
	}
	if e.Op == "" {
		return fmt.Sprintf("%s: %v", e.Category, e.Err)
	}
	return fmt.Sprintf("%s %s: %v", e.Category, e.Op, e.Err)
}

func (e *BackupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ClassifyError(err error) FailureCategory {
	var be *BackupError
	if errors.As(err, &be) {
		return be.Category
	}
	return FailureCommand
}

func StatusForFailure(cat FailureCategory) JobStatus {
	switch cat {
	case FailureConnect:
		return StatusFailedConnect
	case FailureAuth:
		return StatusFailedAuth
	case FailurePrivilege:
		return StatusFailedPrivilege
	case FailureTimeout:
		return StatusFailedTimeout
	case FailureNormalization:
		return StatusFailedNormalization
	case FailureRedaction:
		return StatusFailedRedaction
	case FailureStorage:
		return StatusFailedStorage
	case FailureCredentialProvider:
		return StatusFailedCredentialProvider
	default:
		return StatusFailedCommand
	}
}
