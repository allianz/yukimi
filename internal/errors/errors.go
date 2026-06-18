// Copyright 2026 The Yukimi Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errors

import (
	stderrors "errors"
	"fmt"
)

const (
	maxMsgLen       = 256
	defaultEmptyMsg = "invalid configuration — no details provided"
)

type userError struct {
	msg string
}

func (e *userError) Error() string {
	return e.msg
}

// NewUserError creates a user error with an actionable message.
// Auto-truncates to 256 chars; falls back to a default if msg is empty.
func NewUserError(msg string) error {
	if msg == "" {
		return &userError{msg: defaultEmptyMsg}
	}
	if len(msg) > maxMsgLen {
		msg = fmt.Sprintf("%s...", msg[:maxMsgLen])
	}
	return &userError{msg: msg}
}

// IsUserError reports whether the error chain contains a user error.
func IsUserError(err error) bool {
	var ue *userError
	return stderrors.As(err, &ue)
}
