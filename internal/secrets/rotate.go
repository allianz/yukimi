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
	"fmt"
)

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

	value, err := MarshalCredentials(creds)
	if err != nil {
		return nil, err
	}

	if err := b.Update(ctx, path, value); err != nil {
		return nil, fmt.Errorf("failed to rotate secret at %s: %w", path, err)
	}
	return creds, nil
}
