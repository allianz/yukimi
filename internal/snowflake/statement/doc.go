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

// Package statement executes SQL against an injected Executor, one
// statement per call, materializing rows and decorating failures with
// structured driver fields. It never opens a connection itself (004's job)
// and never decides what SQL to run (every downstream module's job). See
// specs/005-statement-execution.md for the full specification.
package statement
