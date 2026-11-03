//go:build scaletest

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

package scaletest

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testCtx is the context of a test that runs the code under test. It has a deadline,
// so a wait that never ends fails the test in seconds instead of hanging until the
// timeout of go test.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ctxAware is a kube client that, like a real one, fails a request once its context
// is cancelled. The fake client ignores the context.
type ctxAware struct{ client.Client }

func (c ctxAware) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c ctxAware) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.List(ctx, list, opts...)
}

func (c ctxAware) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c ctxAware) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.Update(ctx, obj, opts...)
}

func (c ctxAware) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.Delete(ctx, obj, opts...)
}
