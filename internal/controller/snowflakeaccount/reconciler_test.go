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
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/config/base"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// fakeModule scripts single-shot Observe/Apply/Teardown outcomes and, unlike
// the account module, never touches mc — see
// internal/account/pipeline/pipeline_test.go's own fakeModule, whose shape
// this copies (the type itself is unexported there, so it cannot be
// imported).
type fakeModule struct {
	name          string
	observeInSync bool
	observeOut    pipeline.Outcome
	applyOut      pipeline.Outcome
	teardownErr   error

	forbidTeardown bool
	forbidApply    bool

	applyCalled    int
	teardownCalled int
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Observe(_ context.Context, _ *pipeline.ModuleContext) (bool, pipeline.Outcome) {
	return f.observeInSync, f.observeOut
}

func (f *fakeModule) Apply(_ context.Context, _ *pipeline.ModuleContext) pipeline.Outcome {
	if f.forbidApply {
		panic("Apply must not be called")
	}
	f.applyCalled++
	return f.applyOut
}

func (f *fakeModule) Teardown(_ context.Context, _ *pipeline.ModuleContext) error {
	if f.forbidTeardown {
		panic("Teardown must not be called")
	}
	f.teardownCalled++
	return f.teardownErr
}

// fakeRecorder records every event.Event handed to it, keyed by the object's
// name, so a test can assert what was emitted.
type fakeRecorder struct {
	events []event.Event
}

func (r *fakeRecorder) Event(_ runtime.Object, e event.Event) {
	r.events = append(r.events, e)
}

func (r *fakeRecorder) WithAnnotations(_ ...string) event.Recorder { return r }

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build v1alpha1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build corev1 scheme: %v", err)
	}
	return scheme
}

func newTestCR(name, namespace, region string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.SnowflakeAccountSpec{Region: region},
	}
}

func newTestNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func newTestDeletionRequest(name, namespace, targetName, state string, createdAt time.Time) *v1alpha1.SnowflakeDeletionRequest {
	return &v1alpha1.SnowflakeDeletionRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: v1alpha1.SnowflakeDeletionRequestSpec{
			TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: targetName},
			Reason:    "test",
		},
		Status: v1alpha1.SnowflakeDeletionRequestStatus{State: state},
	}
}

func newExternal(t *testing.T, m pipeline.Module, objs ...client.Object) (*external, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).WithStatusSubresource(&v1alpha1.SnowflakeDeletionRequest{}).Build()
	e := &external{
		kube:     c,
		pool:     nil, // fakeModule never touches the ModuleContext's pool
		pipeline: pipeline.New(m),
		cfg:      &base.Config{Snowflake: base.SnowflakeSettings{UsePrivateLink: true}},
		logger:   logging.NewNopLogger(),
		record:   &fakeRecorder{},
	}
	return e, c
}

// --- Setup / SetupGated -----------------------------------------------------

func newTestManager(t *testing.T) ctrl.Manager {
	t.Helper()
	skipNameValidation := true
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{
		Scheme:     testScheme(t),
		Controller: config.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	return mgr
}

func testBaseConfig() *base.Config {
	return &base.Config{
		Snowflake: base.SnowflakeSettings{Org: "myorg", AccountCreationGracePeriod: time.Minute},
		Deletion:  base.DeletionSettings{GracePeriodDays: 30},
	}
}

// SC-004 (in combination with reconciler.go's source, which calls
// pipeline.New with exactly accountmodule.New(...) as its sole argument):
// Setup succeeds end to end, constructing the real account module and
// registering it as the pipeline's only module.
func TestSetup_Default(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testBaseConfig()
	p := pool.New(secrets.NewFakeBackend(), cfg)
	o := controller.Options{Logger: logging.NewNopLogger(), Features: &feature.Flags{}}

	if err := Setup(mgr, o, cfg, p, secrets.NewFakeBackend()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetup_AllOptionalFeaturesEnabled(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testBaseConfig()
	p := pool.New(secrets.NewFakeBackend(), cfg)

	features := &feature.Flags{}
	features.Enable(feature.EnableBetaManagementPolicies)
	features.Enable(feature.EnableAlphaChangeLogs)

	o := controller.Options{
		Logger:           logging.NewNopLogger(),
		Features:         features,
		ChangeLogOptions: &controller.ChangeLogOptions{},
		MetricOptions:    &controller.MetricOptions{},
	}
	if err := Setup(mgr, o, cfg, p, secrets.NewFakeBackend()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeGate struct {
	registered []schema.GroupVersionKind
	callback   func()
}

func (g *fakeGate) Register(callback func(), gvks ...schema.GroupVersionKind) {
	g.registered = append(g.registered, gvks...)
	g.callback = callback
}

func (g *fakeGate) Set(_ schema.GroupVersionKind, _ bool) bool { return false }

func TestSetupGated_RegistersAndRuns(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testBaseConfig()
	p := pool.New(secrets.NewFakeBackend(), cfg)
	gate := &fakeGate{}
	o := controller.Options{Logger: logging.NewNopLogger(), Features: &feature.Flags{}, Gate: gate}

	if err := SetupGated(mgr, o, cfg, p, secrets.NewFakeBackend()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gate.registered) != 1 || gate.registered[0] != v1alpha1.SnowflakeAccountGroupVersionKind {
		t.Fatalf("expected registration for %v, got %v", v1alpha1.SnowflakeAccountGroupVersionKind, gate.registered)
	}
	if gate.callback == nil {
		t.Fatal("expected a callback to be registered")
	}
	gate.callback() // must not panic
}

func TestConnector_Connect(t *testing.T) {
	c := &connector{logger: logging.NewNopLogger(), record: &fakeRecorder{}, pipeline: pipeline.New()}
	got, err := c.Connect(context.Background(), newTestCR("acct", "ns", "aws-eu-central-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil external client")
	}
}

func TestDisconnect_NoOp(t *testing.T) {
	e := &external{}
	if err := e.Disconnect(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Observe -----------------------------------------------------------------

// SC-006: Observe on a resource being deleted reports ResourceExists based
// solely on AccountLocator and never touches the pipeline or the Kubernetes
// API for namespace labels.
func TestObserve_Deleting_NoLocator_ReleasesFinalizer(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, forbidApply: true}
	e, _ := newExternal(t, m) // no namespace object registered: a Get would fail loudly
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("unexpected observation: %+v", got)
	}
}

func TestObserve_Deleting_WithLocator_KeepsFinalizer(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, forbidApply: true}
	e, _ := newExternal(t, m)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.Status.AccountLocator = "xc19114"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("unexpected observation: %+v", got)
	}
}

// SC-005 (implicit): a namespace-labels lookup failure is a handled system
// error and sets Unavailable.
func TestObserve_NamespaceLabelsError_ReturnsHandledError(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName}
	e, _ := newExternal(t, m) // namespace "ns" was never created
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	_, err := e.Observe(context.Background(), cr)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status == corev1.ConditionTrue {
		t.Fatalf("expected Ready != True, got %+v", got)
	}
}

// Account module reporting inSync == false is the sole source of
// Observation.Exists (009); Observe must report ResourceExists: false without
// rendering any outcome.
func TestObserve_AccountNotExists(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, observeInSync: false, observeOut: pipeline.Pending("not created yet")}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Fatalf("expected ResourceExists=false, got %+v", got)
	}
}

// SC-012 (Observe half) / SC-013: outcomes are rendered in order, status is
// computed from ModuleContext/AccountLocator, and Observe never flips Ready
// to True itself.
func TestObserve_RendersOutcomesAndStatus(t *testing.T) {
	cond := xpv1.Unavailable().WithMessage("module says so")
	evt := event.Normal("SomeReason", "something happened")
	m := &fakeModule{
		name:          pipeline.AccountModuleName,
		observeInSync: true,
		observeOut:    pipeline.Outcome{State: pipeline.StateDone, Condition: &cond, Event: &evt},
	}
	e, _ := newExternal(t, m, newTestNamespace("ns", map[string]string{"department": "acme"}))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.Status.AccountLocator = "xc19114"
	cr.Generation = 3

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists {
		t.Fatalf("unexpected observation: %+v", got)
	}
	if cr.Status.AccountName == "" {
		t.Fatal("expected AccountName to be set")
	}
	if cr.Status.AccountURL == "" {
		t.Fatal("expected AccountURL to be set once AccountLocator is non-empty")
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status == corev1.ConditionTrue {
		t.Fatalf("Observe must never flip Ready to True itself, got %+v", got)
	}
	rec := e.record.(*fakeRecorder)
	if len(rec.events) != 1 || rec.events[0].Reason != "SomeReason" {
		t.Fatalf("expected the module's event to be recorded, got %+v", rec.events)
	}
}

// SC-013: once Ready is already True, a later Pending outcome from some
// other module must not revert it.
func TestObserve_ReadyLatch_AlreadyTrueNotReverted(t *testing.T) {
	accountModule := &fakeModule{name: pipeline.AccountModuleName, observeInSync: true, observeOut: pipeline.Done()}
	other := &fakeModule{name: "other", observeInSync: false, observeOut: pipeline.Pending("waiting on other")}

	e, _ := newExternal(t, accountModule, newTestNamespace("ns", nil))
	e.pipeline = pipeline.New(accountModule, other)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.SetConditions(xpv1.Available())

	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status != corev1.ConditionTrue {
		t.Fatalf("expected Ready to remain True, got %+v", got)
	}
}

func TestObserve_NotReady_SetsUnavailableWithPendingReason(t *testing.T) {
	accountModule := &fakeModule{name: pipeline.AccountModuleName, observeInSync: true, observeOut: pipeline.Done()}
	other := &fakeModule{name: "other", observeInSync: false, observeOut: pipeline.Pending("waiting on other")}

	e, _ := newExternal(t, accountModule, newTestNamespace("ns", nil))
	e.pipeline = pipeline.New(accountModule, other)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cr.GetCondition(xpv1.TypeReady)
	if got.Status == corev1.ConditionTrue || got.Message != "waiting on other" {
		t.Fatalf("expected Unavailable with pending reason, got %+v", got)
	}
}

// --- Create / Update / apply --------------------------------------------------

func TestCreateUpdate_BothDelegateToApply(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Done()}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.applyCalled != 2 {
		t.Fatalf("expected Apply to be called twice (once per Create/Update), got %d", m.applyCalled)
	}
}

// SC-009/SC-010: observedGeneration only advances, and status is populated,
// once Result.AllDone() is true.
func TestApply_AllDone_AdvancesGenerationAndSetsStatus(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Done()}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.Status.AccountLocator = "xc19114"
	cr.Generation = 7

	if err := e.apply(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Status.GetObservedGeneration() != 7 {
		t.Fatalf("expected observedGeneration 7, got %d", cr.Status.GetObservedGeneration())
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status != corev1.ConditionTrue {
		t.Fatalf("expected Ready=True, got %+v", got)
	}
	if cr.Status.AccountName == "" || cr.Status.AccountURL == "" {
		t.Fatalf("expected accountName/accountUrl to be set, got %+v", cr.Status)
	}
}

// SC-011: an aborted Apply leaves observedGeneration untouched.
func TestApply_Aborted_DoesNotAdvanceGeneration(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Failed(assertNewSystemError()).Aborting()}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.Generation = 7
	cr.Status.SetObservedGeneration(5)

	_ = e.apply(context.Background(), cr)

	if cr.Status.GetObservedGeneration() != 5 {
		t.Fatalf("expected observedGeneration to remain 5, got %d", cr.Status.GetObservedGeneration())
	}
}

// SC-008: whenever Result.FirstError() is non-nil, apply (and therefore
// Create/Update) must return the handled error, never nil.
func TestApply_RejectedModule_ReturnsHandledError(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Rejected(assertNewUserError())}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	err := e.apply(context.Background(), cr)
	if err == nil {
		t.Fatal("expected a non-nil error, got nil")
	}

	if _, createErr := e.Create(context.Background(), cr); createErr == nil {
		t.Fatal("expected Create to surface the same non-nil error")
	}
}

// SC-013 (apply half): Ready, once already True, is not reverted to
// Unavailable by a later Pending outcome from some other module.
func TestApply_ReadyLatch_AlreadyTrueNotReverted(t *testing.T) {
	accountModule := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Done()}
	other := &fakeModule{name: "other", applyOut: pipeline.Pending("still syncing")}

	e, _ := newExternal(t, accountModule, newTestNamespace("ns", nil))
	e.pipeline = pipeline.New(accountModule, other)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")
	cr.SetConditions(xpv1.Available())

	if err := e.apply(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status != corev1.ConditionTrue {
		t.Fatalf("expected Ready to remain True, got %+v", got)
	}
}

func TestApply_NamespaceLabelsError_ReturnsHandledError(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, forbidApply: true}
	e, _ := newExternal(t, m) // namespace "ns" never created
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if err := e.apply(context.Background(), cr); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// --- Delete: the deletion gate ----------------------------------------------

// SC-014: no Active SnowflakeDeletionRequest blocks the destruction outright.
func TestDelete_NoActiveRequest_Blocks(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, forbidTeardown: true}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	_, err := e.Delete(context.Background(), cr)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status == corev1.ConditionTrue {
		t.Fatalf("expected Ready != True, got %+v", got)
	}
	rec := e.record.(*fakeRecorder)
	if len(rec.events) != 1 || rec.events[0].Reason != "DeletionBlocked" {
		t.Fatalf("expected a DeletionBlocked event, got %+v", rec.events)
	}
}

// SC-016: a Pipeline.Destroy failure leaves the matched request Active.
func TestDelete_DestroyFails_LeavesRequestActive(t *testing.T) {
	req := newTestDeletionRequest("req", "ns", "acct", "Active", time.Now())
	m := &fakeModule{name: pipeline.AccountModuleName, teardownErr: assertNewSystemError()}
	e, kube := newExternal(t, m, newTestNamespace("ns", nil), req)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	_, err := e.Delete(context.Background(), cr)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var persisted v1alpha1.SnowflakeDeletionRequest
	if getErr := kube.Get(context.Background(), client.ObjectKeyFromObject(req), &persisted); getErr != nil {
		t.Fatalf("unexpected error fetching persisted request: %v", getErr)
	}
	if persisted.Status.State != "Active" {
		t.Fatalf("expected request to remain Active, got %q", persisted.Status.State)
	}
}

// SC-015: a successful Destroy marks the matched request Consumed.
func TestDelete_Success_MarksConsumed(t *testing.T) {
	req := newTestDeletionRequest("req", "ns", "acct", "Active", time.Now())
	m := &fakeModule{name: pipeline.AccountModuleName}
	e, kube := newExternal(t, m, newTestNamespace("ns", nil), req)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.teardownCalled != 1 {
		t.Fatalf("expected Teardown to be called once, got %d", m.teardownCalled)
	}

	var persisted v1alpha1.SnowflakeDeletionRequest
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(req), &persisted); err != nil {
		t.Fatalf("unexpected error fetching persisted request: %v", err)
	}
	if persisted.Status.State != "Consumed" {
		t.Fatalf("expected request to become Consumed, got %q", persisted.Status.State)
	}
}

// SC-017 (defense in depth against a future accidental import): this package
// registers exactly one module today, so Delete against a two-module
// pipeline still tears both down in reverse order.
func TestDelete_TearsDownInReverseOrder(t *testing.T) {
	var order []string
	first := &orderedModule{name: pipeline.AccountModuleName, order: &order}
	second := &orderedModule{name: "other", order: &order}

	req := newTestDeletionRequest("req", "ns", "acct", "Active", time.Now())
	e, _ := newExternal(t, first, newTestNamespace("ns", nil), req)
	e.pipeline = pipeline.New(first, second)
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "other" || order[1] != pipeline.AccountModuleName {
		t.Fatalf("expected teardown in reverse registration order, got %v", order)
	}
}

// orderedModule is a minimal pipeline.Module that only records Teardown call
// order; Observe/Apply are never exercised through it.
type orderedModule struct {
	name  string
	order *[]string
}

func (o *orderedModule) Name() string { return o.name }
func (o *orderedModule) Observe(_ context.Context, _ *pipeline.ModuleContext) (bool, pipeline.Outcome) {
	return true, pipeline.Done()
}
func (o *orderedModule) Apply(_ context.Context, _ *pipeline.ModuleContext) pipeline.Outcome {
	return pipeline.Done()
}
func (o *orderedModule) Teardown(_ context.Context, _ *pipeline.ModuleContext) error {
	*o.order = append(*o.order, o.name)
	return nil
}

func assertNewUserError() error   { return internalerrors.NewUserError("bad input") }
func assertNewSystemError() error { return stderrors.New("boom") }

// updateAccountStatus's tenant.AccountURL error path is logged and
// swallowed, never failing the caller (Edge Cases).
func TestApply_AccountURLError_LoggedAndSwallowed(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, applyOut: pipeline.Done()}
	e, _ := newExternal(t, m, newTestNamespace("ns", nil))
	cr := newTestCR("acct", "ns", "not a region")
	cr.Status.AccountLocator = "xc19114"

	if err := e.apply(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Status.AccountURL != "" {
		t.Fatalf("expected AccountURL to stay unset on a region-format error, got %q", cr.Status.AccountURL)
	}
}

// erroringListClient wraps a client.Client and fails every List call, to
// exercise Delete's FindActiveRequest system-error path.
type erroringListClient struct {
	client.Client
}

func (c *erroringListClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return stderrors.New("simulated API failure")
}

func TestDelete_FindActiveRequestError_ReturnsHandledError(t *testing.T) {
	m := &fakeModule{name: pipeline.AccountModuleName, forbidTeardown: true}
	e, kube := newExternal(t, m, newTestNamespace("ns", nil))
	e.kube = &erroringListClient{Client: kube}
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Delete(context.Background(), cr); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// erroringGetClient wraps a client.Client and fails every Get call (the
// namespace-labels lookup), to exercise Delete's post-gate error path.
type erroringGetClient struct {
	client.Client
}

func (c *erroringGetClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return stderrors.New("simulated API failure")
}

func TestDelete_NamespaceLabelsError_AfterActiveRequest(t *testing.T) {
	req := newTestDeletionRequest("req", "ns", "acct", "Active", time.Now())
	m := &fakeModule{name: pipeline.AccountModuleName, forbidTeardown: true}
	e, kube := newExternal(t, m, req) // no namespace object: Get fails naturally, but wrap anyway for clarity
	e.kube = &erroringGetClient{Client: kube}
	cr := newTestCR("acct", "ns", "aws-eu-central-1")

	if _, err := e.Delete(context.Background(), cr); err == nil {
		t.Fatal("expected an error, got nil")
	}
}
