//go:build e2e

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

package support

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Poll calls cond every interval until it reports true, fails with an error
// that says what was awaited, or the timeout passes. It never sleeps blindly:
// the condition is checked first, and the last error of the condition is part
// of the timeout message so a failure is diagnosable.
func Poll(ctx context.Context, timeout, interval time.Duration, what string, cond func(context.Context) (bool, error)) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		ok, err := cond(ctx)
		if err == nil && ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return fmt.Errorf("interrupted while waiting for %s", what)
			}
			if lastErr != nil {
				return fmt.Errorf("timed out after %s waiting for %s (last error: %w)", timeout, what, lastErr)
			}
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		case <-ticker.C:
		}
	}
}

// Eventually is Poll for tests: it fails the test with the same message.
func Eventually(ctx context.Context, t *testing.T, timeout, interval time.Duration, what string, cond func(context.Context) (bool, error)) {
	t.Helper()
	if err := Poll(ctx, timeout, interval, what, cond); err != nil {
		t.Fatal(err)
	}
}
