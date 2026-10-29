//                           _       _
// __      _____  __ ___   ___  __ _| |_ ___
// \ \ /\ / / _ \/ _` \ \ / / |/ _` | __/ _ \
//  \ V  V /  __/ (_| |\ V /| | (_| | ||  __/
//   \_/\_/ \___|\__,_| \_/ |_|\__,_|\__\___|
//
//  Copyright © 2016 - 2026 Weaviate B.V. All rights reserved.
//
//  CONTACT: hello@weaviate.io
//

package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/runtime/middleware"
	"github.com/go-openapi/strfmt"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restCtx "github.com/weaviate/weaviate/adapters/handlers/rest/context"
	"github.com/weaviate/weaviate/adapters/handlers/rest/operations/batch"
	"github.com/weaviate/weaviate/entities/additional"
	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/schema"
	"github.com/weaviate/weaviate/entities/schema/crossref"
	"github.com/weaviate/weaviate/usecases/namespaces"
	"github.com/weaviate/weaviate/usecases/objects"
)

// Pins that referencesResponse echoes From/To on every row regardless of
// ref.Err — clients correlate failures back to the input by beacon, not
// by slice index (the slice can be reordered upstream).
func TestReferencesResponse_FailedRowsCarryBeacons(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	from := crossref.NewSource(schema.ClassName("Zoo"), "hasAnimals", strfmt.UUID(uuid))
	to := &crossref.Ref{
		Local:    true,
		PeerName: "localhost",
		Class:    "Animal",
		TargetID: strfmt.UUID(uuid),
	}

	input := objects.BatchReferences{
		{From: from, To: to}, // succeeds
		{From: from, To: to, Err: errors.New("validation: ref target missing")}, // fails
	}

	h := &batchObjectHandlers{}
	got := h.referencesResponse(nil, input)

	require.Len(t, got, 2)

	// Both rows must carry beacons.
	assert.NotEmpty(t, got[0].From, "success row must carry From beacon")
	assert.NotEmpty(t, got[0].To, "success row must carry To beacon")
	assert.NotEmpty(t, got[1].From, "FAILED row must still carry From beacon for correlation")
	assert.NotEmpty(t, got[1].To, "FAILED row must still carry To beacon for correlation")

	// Statuses are as expected.
	require.NotNil(t, got[0].Result.Status)
	require.NotNil(t, got[1].Result.Status)
	assert.Equal(t, models.BatchReferenceResponseAO1ResultStatusSUCCESS, *got[0].Result.Status)
	assert.Equal(t, models.BatchReferenceResponseAO1ResultStatusFAILED, *got[1].Result.Status)
	assert.Nil(t, got[0].Result.Errors)
	assert.NotNil(t, got[1].Result.Errors)
}

// Pins that nil From/To on a failed row don't panic — the strip helpers
// must tolerate nil inputs (return ""). This covers the rejection path
// where upstream fails before populating From/To.
func TestReferencesResponse_FailedRowWithNilBeaconsDoesNotPanic(t *testing.T) {
	input := objects.BatchReferences{
		{Err: errors.New("rejected before From/To set")},
	}

	h := &batchObjectHandlers{}
	got := h.referencesResponse(nil, input)

	require.Len(t, got, 1)
	require.NotNil(t, got[0].Result.Status)
	assert.Equal(t, models.BatchReferenceResponseAO1ResultStatusFAILED, *got[0].Result.Status)
	// Empty beacons are acceptable here — what matters is no panic and the
	// failure is preserved in Result.Errors.
	assert.NotNil(t, got[0].Result.Errors)
}

// stubRequestsTotal swallows the metric calls addObjects makes on its error
// paths.
type stubRequestsTotal struct{}

func (stubRequestsTotal) logError(string, error)       {}
func (stubRequestsTotal) logOk(string)                 {}
func (stubRequestsTotal) logUserError(string)          {}
func (stubRequestsTotal) logServerError(string, error) {}

func TestBatchObjectHandlers_AddObjects(t *testing.T) {
	t.Run("records the caller's namespace before validation rejects the batch", func(t *testing.T) {
		tests := []struct {
			name      string
			principal *models.Principal
			want      string
		}{
			{name: "nil principal yields empty label", principal: nil, want: ""},
			{
				name:      "global operator yields empty label",
				principal: &models.Principal{Username: "admin", Namespace: "ns_a", IsGlobalOperator: true},
				want:      "",
			},
			{
				name:      "namespace-less principal yields empty label",
				principal: &models.Principal{Username: "legacy"},
				want:      "",
			},
			{
				name:      "namespaced user yields its namespace",
				principal: &models.Principal{Username: "ns_a:alice", Namespace: "ns_a"},
				want:      "ns_a",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx, slot := restCtx.WithBatchNamespaceSlot(context.Background())
				r := httptest.NewRequest(http.MethodPost, "/v1/batch/objects", nil).WithContext(ctx)
				// An unknown consistency level fails validation on the line
				// after the slot write, which keeps the batch manager out of
				// this test.
				badLevel := "not-a-level"

				h := &batchObjectHandlers{metricRequestsTotal: stubRequestsTotal{}}
				res := h.addObjects(batch.BatchObjectsCreateParams{
					HTTPRequest:      r,
					ConsistencyLevel: &badLevel,
					Body:             batch.BatchObjectsCreateBody{},
				}, tc.principal)

				require.IsType(t, &batch.BatchObjectsCreateBadRequest{}, res)
				assert.Equal(t, tc.want, slot.Namespace)
			})
		}
	})

	// A suspended namespace refuses the tenant autoTenants creates for a batch,
	// which fails the whole request rather than one row. The metric counts it as
	// a user error, so it logs nothing.
	t.Run("answers 422 for a namespace refusal", func(t *testing.T) {
		logger, hook := logrustest.NewNullLogger()
		h := &batchObjectHandlers{
			manager:             &fakeBatchManager{err: fmt.Errorf("auto create tenants: %w", namespaces.ErrNamespaceSuspended)},
			metricRequestsTotal: newBatchRequestsTotal(nil, logger),
		}
		rec := httptest.NewRecorder()
		h.addObjects(batch.BatchObjectsCreateParams{
			HTTPRequest: httptest.NewRequest(http.MethodPost, "/v1/batch/objects", nil),
			Body:        batch.BatchObjectsCreateBody{Objects: []*models.Object{{Class: "alpha:Movies"}}},
		}, nil).WriteResponse(rec, runtime.JSONProducer())

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "namespace is suspended")
		assert.Empty(t, hook.AllEntries())
	})
}

// fakeBatchManager answers AddObjects with err. Its other methods panic.
type fakeBatchManager struct {
	batchObjectsManager
	err error
}

func (f *fakeBatchManager) AddObjects(context.Context, *models.Principal, []*models.Object,
	[]*string, *additional.ReplicationProperties,
) (objects.BatchObjects, error) {
	return nil, f.err
}

// routingBatchManager records which batch entrypoint the handler chose.
type routingBatchManager struct {
	batchObjectsManager
	addCalled      bool
	validateCalled bool
	result         objects.BatchObjects
}

func (f *routingBatchManager) AddObjects(context.Context, *models.Principal, []*models.Object,
	[]*string, *additional.ReplicationProperties,
) (objects.BatchObjects, error) {
	f.addCalled = true
	return f.result, nil
}

func (f *routingBatchManager) ValidateObjects(context.Context, *models.Principal, []*models.Object,
	[]*string, *additional.ReplicationProperties,
) (objects.BatchObjects, error) {
	f.validateCalled = true
	return f.result, nil
}

func validateBoolPtr(v bool) *bool { return &v }

// Pins that validate=true routes to ValidateObjects while the absent flag and
// validate=false keep the regular create path; both reuse the same response.
func TestBatchObjectHandlers_AddObjects_ValidateRouting(t *testing.T) {
	tests := []struct {
		name         string
		validate     *bool
		wantValidate bool
		wantAdd      bool
	}{
		{name: "flag absent creates", validate: nil, wantAdd: true},
		{name: "flag false creates", validate: validateBoolPtr(false), wantAdd: true},
		{name: "flag true validates", validate: validateBoolPtr(true), wantValidate: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &routingBatchManager{result: objects.BatchObjects{}}
			h := &batchObjectHandlers{manager: mgr, metricRequestsTotal: stubRequestsTotal{}}

			ctx, _ := restCtx.WithBatchNamespaceSlot(context.Background())
			r := httptest.NewRequest(http.MethodPost, "/v1/batch/objects", nil).WithContext(ctx)

			res := h.addObjects(batch.BatchObjectsCreateParams{
				HTTPRequest: r,
				Validate:    tc.validate,
				Body: batch.BatchObjectsCreateBody{
					Objects: []*models.Object{{Class: "Foo"}},
				},
			}, nil)

			require.IsType(t, &batch.BatchObjectsCreateOK{}, res)
			assert.Equal(t, tc.wantAdd, mgr.addCalled)
			assert.Equal(t, tc.wantValidate, mgr.validateCalled)
		})
	}
}

// Pins the generated binder for the validate query parameter.
func TestBatchObjectsCreateParams_BindValidate(t *testing.T) {
	route := &middleware.MatchedRoute{Consumer: runtime.JSONConsumer()}

	bind := func(rawQuery string) (*bool, error) {
		r := httptest.NewRequest(http.MethodPost, "/v1/batch/objects?"+rawQuery,
			strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		var p batch.BatchObjectsCreateParams
		err := p.BindRequest(r, route)
		return p.Validate, err
	}

	v, err := bind("validate=true")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.True(t, *v)

	v, err = bind("validate=false")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.False(t, *v)

	v, err = bind("")
	require.NoError(t, err)
	assert.Nil(t, v, "the parameter stays unset when absent")

	// The binder follows go-openapi's permissive bool conversion; the spec
	// validation middleware rejects non-boolean values before the handler.
	v, err = bind("validate=maybe")
	require.NoError(t, err)
	require.NotNil(t, v)
	assert.False(t, *v)
}

// A row rejected before it ever carried an object (empty class, null entry)
// must still render as FAILED instead of panicking on a nil object.
func TestObjectsResponse_FailedRowWithoutObject(t *testing.T) {
	h := &batchObjectHandlers{}
	input := objects.BatchObjects{
		{OriginalIndex: 0, Err: errors.New("object has an empty class")},
		{
			OriginalIndex: 1,
			Object:        &models.Object{Class: "Foo"},
			UUID:          "11111111-2222-3333-4444-555555555555",
		},
	}

	got := h.objectsResponse(nil, input)

	require.Len(t, got, 2)
	require.NotNil(t, got[0].Result.Status)
	assert.Equal(t, models.ObjectsGetResponseAO2ResultStatusFAILED, *got[0].Result.Status)
	assert.NotNil(t, got[0].Result.Errors)
	require.NotNil(t, got[1].Result.Status)
	assert.Equal(t, models.ObjectsGetResponseAO2ResultStatusSUCCESS, *got[1].Result.Status)
	assert.Nil(t, got[1].Result.Errors)
	assert.Equal(t, strfmt.UUID("11111111-2222-3333-4444-555555555555"), got[1].Object.ID)
}
