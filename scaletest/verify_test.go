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
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/allianz/yukimi/internal/snowflake/statement"
)

func TestScaletestAccounts(t *testing.T) {
	rows := []statement.Row{
		{"account_name": "SCALETEST_00001_ABCD"},
		{"account_name": "scaletest_00002_abcd"},
		{"account_name": "SCALETESTER_X"}, // LIKE 'scaletest%' matches it, the prefix with '_' does not
		{"account_name": "PLATFORM"},
		{"comment": "no name"},
	}
	got := scaletestAccounts(rows)
	want := []string{"SCALETEST_00001_ABCD", "scaletest_00002_abcd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
}

func TestListedNotScheduled(t *testing.T) {
	now := time.Now()
	entries := []smtypes.SecretListEntry{
		{Name: aws.String("yk-o--scaletest--a")},
		{Name: aws.String("yk-o--scaletest--b"), DeletedDate: &now}, // scheduled
		{Name: aws.String("yk-o--other--c")},                        // another namespace
	}
	got := listedNotScheduled(entries, "yk-o--scaletest--")
	if !reflect.DeepEqual(got, []string{"yk-o--scaletest--a"}) {
		t.Fatalf("names = %v", got)
	}
}

// fakeSecrets lists the pages of entries and describes them from the current state.
type fakeSecrets struct {
	pages       [][]smtypes.SecretListEntry
	current     map[string]*time.Time // the up-to-date deletion date; a missing key is a secret that is gone
	listErr     error
	describeErr error
}

func (f fakeSecrets) ListSecrets(_ context.Context, in *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	page := 0
	if in.NextToken != nil {
		page, _ = strconv.Atoi(*in.NextToken)
	}
	out := &secretsmanager.ListSecretsOutput{SecretList: f.pages[page]}
	if page+1 < len(f.pages) {
		out.NextToken = aws.String(strconv.Itoa(page + 1))
	}
	return out, nil
}

func (f fakeSecrets) DescribeSecret(_ context.Context, in *secretsmanager.DescribeSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	date, ok := f.current[aws.ToString(in.SecretId)]
	if !ok {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.DescribeSecretOutput{DeletedDate: date}, nil
}

func TestNotScheduledSecretsTrustsDescribeSecretOverTheLaggingList(t *testing.T) {
	now := time.Now()
	const prefix = "yk-o--scaletest--"
	api := fakeSecrets{
		// The list lags: it shows all three as not scheduled.
		pages: [][]smtypes.SecretListEntry{{
			{Name: aws.String(prefix + "scheduled")},
			{Name: aws.String(prefix + "gone")},
			{Name: aws.String(prefix + "left")},
		}},
		current: map[string]*time.Time{prefix + "scheduled": &now, prefix + "left": nil},
	}

	got, err := notScheduledSecrets(context.Background(), api, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{prefix + "left"}) {
		t.Fatalf("left = %v, want only the secret that is really not scheduled", got)
	}
}

func TestNotScheduledSecretsReportsErrors(t *testing.T) {
	if _, err := notScheduledSecrets(context.Background(), fakeSecrets{listErr: errors.New("denied")}, "p"); err == nil {
		t.Fatal("expected the listing error")
	}
}

func TestNotScheduledSecretsReadsEveryPage(t *testing.T) {
	const prefix = "yk-o--scaletest--"
	api := fakeSecrets{
		pages: [][]smtypes.SecretListEntry{
			{{Name: aws.String(prefix + "a")}},
			{{Name: aws.String(prefix + "b")}},
			{{Name: aws.String(prefix + "c")}},
		},
		current: map[string]*time.Time{prefix + "a": nil, prefix + "b": nil, prefix + "c": nil},
	}
	got, err := notScheduledSecrets(context.Background(), api, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{prefix + "a", prefix + "b", prefix + "c"}) {
		t.Fatalf("left = %v, want one from each page", got)
	}
}

func TestNotScheduledSecretsDoesNotGuessWhenDescribeFails(t *testing.T) {
	const prefix = "yk-o--scaletest--"
	api := fakeSecrets{
		pages:       [][]smtypes.SecretListEntry{{{Name: aws.String(prefix + "a")}}},
		describeErr: errors.New("throttled"),
	}
	if _, err := notScheduledSecrets(context.Background(), api, prefix); err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("err = %v, want the lookup error: a secret of unknown state is neither clean nor left", err)
	}
}
