/*
Copyright 2020 The Crossplane Authors.
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

package controller

import (
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/allianz/yukimi/internal/controller/snowflakeaccount"
)

// Dependencies are the runtime collaborators cmd/provider/main.go constructs
// once at startup and forwards, unchanged, into each resource type's own
// Dependencies. Grows one field per additional resource type as later specs
// land.
type Dependencies struct {
	SnowflakeAccount snowflakeaccount.Dependencies
}

// SetupGated creates all Yukimi controllers with safe-start support and adds them to
// the supplied manager.
func SetupGated(mgr ctrl.Manager, o controller.Options, deps Dependencies) error {
	return snowflakeaccount.SetupGated(mgr, o, deps.SnowflakeAccount)
}
