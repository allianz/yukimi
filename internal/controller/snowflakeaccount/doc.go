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

// Package snowflakeaccount reconciles SnowflakeAccount objects: it wires the
// account pipeline (009) into the four managed-resource entry points and
// implements the deletion gate that requires an Active SnowflakeDeletionRequest
// (019) before dropping a live account. See specs/020-snowflakeaccount-controller.md.
package snowflakeaccount
