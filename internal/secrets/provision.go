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

package secrets

import (
	"context"
	stderrors "errors"
	"fmt"
)

// CreateOrRecover stores newCreds at path, resolving whichever already-there
// condition it can decide on its own and surfacing the one it cannot. See Key
// Concept: Recovering From a Non-Atomic Provisioning Sequence.
//
// Parameters:
//   - newCreds: freshly generated credentials the caller wants stored if
//     nothing is already there
//
// Returns:
//   - stored: newCreds on a clean create or a pending-deletion recovery;
//     the value already at path (never newCreds) when existed is true
//   - existed: true only when Create found a live ErrAlreadyExists — the
//     caller (010) must combine this with its own knowledge of whether the
//     Snowflake account exists to decide whether reuse is safe
//   - err: System error for anything Create/Purge/Get returns that this
//     function does not resolve itself (ErrDenied, ErrUnavailable, a second
//     failed Create after a Purge, or an unclassified store fault)
func CreateOrRecover(ctx context.Context, b Backend, path Path, newCreds *Credentials) (stored *Credentials, existed bool, err error) {
	data, err := MarshalCredentials(newCreds)
	if err != nil {
		return nil, false, err
	}

	createErr := b.Create(ctx, path, data)
	if stderrors.Is(createErr, ErrPendingDeletion) {
		if err := b.Purge(ctx, path); err != nil {
			return nil, false, fmt.Errorf("failed to purge pending-deletion secret at %s: %w", path, err)
		}
		// Retry once. A second ErrPendingDeletion here (an extremely unlikely
		// double-race) escalates rather than looping; any other outcome —
		// including a fresh ErrAlreadyExists from another caller's own
		// purge-then-recreate landing in between — falls through to the
		// same handling below as if it were the original attempt's result.
		createErr = b.Create(ctx, path, data)
		if stderrors.Is(createErr, ErrPendingDeletion) {
			return nil, false, fmt.Errorf("failed to create secret at %s after purge: %w", path, createErr)
		}
	}

	switch {
	case createErr == nil:
		return newCreds, false, nil

	case stderrors.Is(createErr, ErrAlreadyExists):
		// Deliberately Get, never Update — an existing secret found here
		// belongs to whoever created it; recovering it is the caller's job.
		raw, err := b.Get(ctx, path)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read existing secret at %s: %w", path, err)
		}
		existing, err := UnmarshalCredentials(raw)
		if err != nil {
			return nil, false, err
		}
		return existing, true, nil

	default:
		return nil, false, fmt.Errorf("failed to create secret at %s: %w", path, createErr)
	}
}

// Rotate generates a fresh keypair and overwrites whatever is stored at path.
// Update semantics: it fails if nothing is there yet — rotation only ever
// replaces a live credential, never creates one. Pushing the new public key
// into Snowflake (ALTER USER ... SET RSA_PUBLIC_KEY) is the caller's job once
// the connection pool (004) exists; this function only makes key replacement
// available as a primitive.
//
// Returns:
//   - System error if nothing is stored at path, or if the store or key
//     generation fails
func Rotate(ctx context.Context, b Backend, path Path, username string) (*Credentials, error) {
	creds, err := NewCredentials(username)
	if err != nil {
		return nil, err
	}

	data, err := MarshalCredentials(creds)
	if err != nil {
		return nil, err
	}

	if err := b.Update(ctx, path, data); err != nil {
		return nil, fmt.Errorf("failed to rotate secret at %s: %w", path, err)
	}
	return creds, nil
}
