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

// Package snowflakedeletionrequest reconciles SnowflakeDeletionRequest
// objects, advancing their own time-boxed Active/Expired/Consumed
// lifecycle independently of every other reconcile loop in this platform.
// See specs/019-deletion-request.md for the full specification.
package snowflakedeletionrequest
