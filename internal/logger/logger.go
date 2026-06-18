/*
Copyright 2026 The Yukimi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logger

import (
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/google/uuid"

	"github.com/allianz/yukimi/internal/errors"
)

// Operation identifies the controller lifecycle method in which an error occurred.
type Operation string

const (
	OpObserve Operation = "observe"
	OpCreate  Operation = "create"
	OpUpdate  Operation = "update"
	OpDelete  Operation = "delete"
)

// Logger holds contextual dimensions for a single controller method invocation.
type Logger struct {
	log       logging.Logger
	namespace string
	kind      string
	name      string
	op        Operation
}

// New creates a Logger pre-loaded with the contextual dimensions for a
// single controller method invocation.
func New(log logging.Logger, namespace, kind, name string, op Operation) *Logger {
	return &Logger{
		log:       log,
		namespace: namespace,
		kind:      kind,
		name:      name,
		op:        op,
	}
}

func (l *Logger) kvs() []any {
	return []any{
		"namespace", l.namespace,
		"kind", l.kind,
		"name", l.name,
		"operation", string(l.op),
	}
}

// Info logs an informational message with all contextual dimensions.
func (l *Logger) Info(msg string) {
	l.log.Info(msg, l.kvs()...)
}

// Debug logs a diagnostic message with all contextual dimensions.
func (l *Logger) Debug(msg string) {
	l.log.Debug(msg, l.kvs()...)
}

// Handle classifies err, logs at the appropriate level, and returns the
// retry error for Crossplane. Returns nil immediately if err is nil.
func (l *Logger) Handle(err error) error {
	if err == nil {
		return nil
	}

	if errors.IsUserError(err) {
		l.log.Debug(err.Error(), l.kvs()...)
		return fmt.Errorf("%s", err.Error()) //nolint:goerr113
	}

	incidentID := generateIncidentID()
	kvs := append(l.kvs(), "incidentID", incidentID, "error", err.Error())
	l.log.Info(fmt.Sprintf("system error (incidentID=%s): %s", incidentID, err.Error()), kvs...)
	return fmt.Errorf("An internal error occurred (%s)", incidentID) //nolint:goerr113
}

func generateIncidentID() string {
	return uuid.New().String()[:8]
}
