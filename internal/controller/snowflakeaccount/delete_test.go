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

package snowflakeaccount

import (
	"context"
	stderrors "errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/tenant"
)

// SC-012: BareIdentifier rejects a resolved name that does not start with a
// letter (a k8s resource name may start with a digit; tenant.ResolveName's
// '-'→'_' translation does not fix that up).
func TestDelete_InvalidResolvedName(t *testing.T) {
	e := &external{log: logging.NewNopLogger(), deps: Dependencies{Pool: &fakePool{}}}
	cr := &v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Name: "123account", Namespace: "team-a"}}

	if _, err := e.Delete(context.Background(), cr); err == nil {
		t.Fatal("Delete() error = nil, want error")
	}
}

func TestDelete_OrgAdminFails(t *testing.T) {
	e := &external{log: logging.NewNopLogger(), deps: Dependencies{Pool: &fakePool{orgAdminErr: internalerrors.NewUserError("no org admin")}}}
	cr := newTestCR("team-a", "aws-eu-central-1")

	if _, err := e.Delete(context.Background(), cr); err == nil {
		t.Fatal("Delete() error = nil, want error")
	}
}

// SC-012: Delete issues exactly one
// DROP ACCOUNT IF EXISTS <resolvedName> GRACE_PERIOD_IN_DAYS = 3 statement
// over the org-admin connection, unconditionally.
func TestDelete_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	defer db.Close()

	cr := newTestCR("team-a", "aws-eu-central-1")
	resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)
	wantSQL := "DROP ACCOUNT IF EXISTS " + resolvedName + " GRACE_PERIOD_IN_DAYS = 3"
	mock.ExpectExec(wantSQL).WillReturnResult(sqlmock.NewResult(0, 0))

	e := &external{log: logging.NewNopLogger(), deps: Dependencies{Pool: &fakePool{orgAdminDB: db}}}
	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// SC-014: a DROP ACCOUNT failure is returned via log.Handle with no
// additional classification.
func TestDelete_ExecFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	defer db.Close()

	cr := newTestCR("team-a", "aws-eu-central-1")
	resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)
	wantSQL := "DROP ACCOUNT IF EXISTS " + resolvedName + " GRACE_PERIOD_IN_DAYS = 3"
	mock.ExpectExec(wantSQL).WillReturnError(stderrors.New("retention-locked snapshot"))

	e := &external{log: logging.NewNopLogger(), deps: Dependencies{Pool: &fakePool{orgAdminDB: db}}}
	if _, err := e.Delete(context.Background(), cr); err == nil {
		t.Fatal("Delete() error = nil, want error")
	}
}
