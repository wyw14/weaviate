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

package batch_delete_limit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/client/batch"
	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/schema"
	"github.com/weaviate/weaviate/test/docker"
	"github.com/weaviate/weaviate/test/helper"
)

const (
	className = "BatchDeleteLimit"
	// queryMaximumResults is what the container runs with. The deployed default is
	// 10000, which no acceptance test can cross in reasonable time.
	queryMaximumResults = 10
	objectCount         = 50
	// drainCallLimit stops a non-terminating drain from hanging the suite.
	drainCallLimit = 20

	maxMatchesClassName = "BatchDeleteMaxMatches"
)

// TestBatchDeleteObjectsStopsAtTheLimit walks the batch delete bound over the REST API:
// one call's deletions, its reply, and that repeating it drains every match.
func TestBatchDeleteObjectsStopsAtTheLimit(t *testing.T) {
	ctx := context.Background()

	compose, err := docker.New().
		WithWeaviate().
		WithWeaviateEnv("QUERY_MAXIMUM_RESULTS", strconv.Itoa(queryMaximumResults)).
		Start(ctx)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, compose.Terminate(ctx))
	}()

	helper.SetupClient(compose.GetWeaviate().URI())
	helper.CreateClass(t, &models.Class{
		Class:      className,
		Vectorizer: "none",
		Properties: []*models.Property{
			{Name: "stringProp", DataType: schema.DataTypeText.PropString()},
		},
	})
	defer helper.DeleteClass(t, className)

	objs := make([]*models.Object, objectCount)
	for i := range objs {
		objs[i] = &models.Object{
			Class:      className,
			ID:         strfmt.UUID(fmt.Sprintf("8d5a3aa2-3c8d-4589-9ae1-3f638f506%03d", i)),
			Properties: map[string]interface{}{"stringProp": fmt.Sprintf("element %d", i)},
		}
	}
	helper.CreateObjectsBatch(t, objs)

	t.Run("a dry run reports the bound and deletes nothing", func(t *testing.T) {
		results := batchDeleteAll(t, true)

		require.Equal(t, int64(queryMaximumResults+1), results.Matches,
			"the count stops one above the limit")
		require.Len(t, results.Objects, queryMaximumResults)
		require.Equal(t, int64(queryMaximumResults), results.Limit)
		require.Equal(t, int64(0), results.Successful)
	})

	t.Run("repeating the call deletes every matching object", func(t *testing.T) {
		deleted := int64(0)
		calls := 0
		for ; calls < drainCallLimit; calls++ {
			results := batchDeleteAll(t, false)
			if results.Matches == 0 {
				break
			}
			require.NotEmpty(t, results.Objects, "a non-zero match count must delete something")
			require.LessOrEqual(t, results.Successful, int64(queryMaximumResults))
			require.Equal(t, int64(0), results.Failed)
			deleted += results.Successful
		}

		require.Less(t, calls, drainCallLimit, "the drain never reported zero matches")
		require.Equal(t, int64(objectCount), deleted)
	})
}

func batchDeleteAll(t *testing.T, dryRun bool) *models.BatchDeleteResponseResults {
	t.Helper()

	output := "verbose"
	all := "*"
	resp, err := helper.Client(t).Batch.BatchObjectsDelete(
		batch.NewBatchObjectsDeleteParams().WithBody(&models.BatchDelete{
			DryRun: &dryRun,
			Output: &output,
			Match: &models.BatchDeleteMatch{
				Class: className,
				Where: &models.WhereFilter{
					Operator:  models.WhereFilterOperatorLike,
					Path:      []string{"id"},
					ValueText: &all,
				},
			},
		}),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, resp.Payload.Results)

	return resp.Payload.Results
}

// TestBatchDeleteObjectsMaxMatchesThreshold exercises the REST contract of the
// optional safety threshold: equality runs the whole resolved plan, anything above
// is a 422 before a delete is sent, an explicit zero only admits a zero match, a dry
// run applies the same threshold, and a threshold above QUERY_MAXIMUM_RESULTS is
// rejected before resolving.
func TestBatchDeleteObjectsMaxMatchesThreshold(t *testing.T) {
	ctx := context.Background()
	const matchedObjects = 3

	compose, err := docker.New().
		WithWeaviate().
		WithWeaviateEnv("QUERY_MAXIMUM_RESULTS", strconv.Itoa(queryMaximumResults)).
		Start(ctx)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, compose.Terminate(ctx))
	}()

	helper.SetupClient(compose.GetWeaviate().URI())
	helper.CreateClass(t, &models.Class{
		Class:      maxMatchesClassName,
		Vectorizer: "none",
		Properties: []*models.Property{
			{Name: "stringProp", DataType: schema.DataTypeText.PropString()},
		},
	})
	defer helper.DeleteClass(t, maxMatchesClassName)

	objs := make([]*models.Object, matchedObjects)
	for i := range objs {
		objs[i] = &models.Object{
			Class:      maxMatchesClassName,
			ID:         strfmt.UUID(fmt.Sprintf("9d5a3aa2-3c8d-4589-9ae1-3f638f507%03d", i)),
			Properties: map[string]interface{}{"stringProp": fmt.Sprintf("element %d", i)},
		}
	}
	helper.CreateObjectsBatch(t, objs)

	// remaining dry-runs through the protected path: while the threshold is at or
	// above the number of matches the plan runs, so this both asserts the count and
	// proves earlier rejections deleted nothing.
	remaining := func(t *testing.T, n int64) {
		t.Helper()
		results, err := batchDeleteWithMaxMatches(t, true, &n, true)
		require.NoError(t, err)
		require.Equal(t, int64(matchedObjects), results.Matches)
		require.Len(t, results.Objects, matchedObjects)
	}

	requireUnprocessable := func(t *testing.T, results *models.BatchDeleteResponseResults, err error, messageHint string) {
		t.Helper()
		require.Error(t, err)
		require.Nil(t, results)
		var unproc *batch.BatchObjectsDeleteUnprocessableEntity
		require.True(t, errors.As(err, &unproc), "expected 422 UnprocessableEntity, got %T: %v", err, err)
		require.NotEmpty(t, unproc.Payload.Error)
		assert.Contains(t, unproc.Payload.Error[0].Message, messageHint)
	}

	t.Run("a negative threshold is rejected as invalid input", func(t *testing.T) {
		negative := int64(-1)
		results, err := batchDeleteWithMaxMatches(t, false, &negative, true)
		requireUnprocessable(t, results, err, "maxMatches")
	})

	t.Run("a threshold above QUERY_MAXIMUM_RESULTS is rejected before resolving", func(t *testing.T) {
		above := int64(queryMaximumResults + 1)
		results, err := batchDeleteWithMaxMatches(t, false, &above, true)
		requireUnprocessable(t, results, err, "QUERY_MAXIMUM_RESULTS")
		remaining(t, queryMaximumResults)
	})

	t.Run("an explicit zero admits a zero match", func(t *testing.T) {
		zero := int64(0)
		results, err := batchDeleteWithMaxMatches(t, false, &zero, false)
		require.NoError(t, err)
		require.Equal(t, int64(0), results.Matches)
		require.Empty(t, results.Objects)
	})

	t.Run("an explicit zero rejects any match and deletes nothing", func(t *testing.T) {
		zero := int64(0)
		results, err := batchDeleteWithMaxMatches(t, false, &zero, true)
		requireUnprocessable(t, results, err, "maxMatches")
		remaining(t, queryMaximumResults)
	})

	t.Run("a dry run over the threshold is rejected like a real delete", func(t *testing.T) {
		below := int64(matchedObjects - 1)
		results, err := batchDeleteWithMaxMatches(t, true, &below, true)
		requireUnprocessable(t, results, err, "maxMatches")
		remaining(t, queryMaximumResults)
	})

	t.Run("a dry run at the threshold reports the set and deletes nothing", func(t *testing.T) {
		at := int64(matchedObjects)
		results, err := batchDeleteWithMaxMatches(t, true, &at, true)
		require.NoError(t, err)
		require.Equal(t, int64(matchedObjects), results.Matches)
		require.Len(t, results.Objects, matchedObjects)
		require.Equal(t, int64(0), results.Successful)
		remaining(t, queryMaximumResults)
	})

	t.Run("more matches than the threshold rejects before any delete", func(t *testing.T) {
		below := int64(matchedObjects - 1)
		results, err := batchDeleteWithMaxMatches(t, false, &below, true)
		requireUnprocessable(t, results, err, "maxMatches")
		remaining(t, queryMaximumResults)
	})

	t.Run("matches at the threshold delete every resolved object", func(t *testing.T) {
		at := int64(matchedObjects)
		results, err := batchDeleteWithMaxMatches(t, false, &at, true)
		require.NoError(t, err)
		require.Equal(t, int64(matchedObjects), results.Matches)
		require.Equal(t, int64(matchedObjects), results.Successful)
		require.Equal(t, int64(0), results.Failed)

		zero := int64(0)
		after, err := batchDeleteWithMaxMatches(t, true, &zero, true)
		require.NoError(t, err, "an explicit zero is fine once nothing matches anymore")
		require.Equal(t, int64(0), after.Matches)
	})
}

// batchDeleteWithMaxMatches issues one batch delete against the threshold class.
// matchAll switches between matching every object (LIKE id "*") and matching no
// object (an Equal on a value nobody has); maxMatches nil sends the field omitted.
func batchDeleteWithMaxMatches(t *testing.T, dryRun bool, maxMatches *int64, matchAll bool,
) (*models.BatchDeleteResponseResults, error) {
	t.Helper()

	output := "verbose"
	where := &models.WhereFilter{
		Path: []string{"stringProp"}, ValueText: ptString("no-such-object"),
		Operator: models.WhereFilterOperatorEqual,
	}
	if matchAll {
		all := "*"
		where = &models.WhereFilter{
			Path: []string{"id"}, ValueText: &all,
			Operator: models.WhereFilterOperatorLike,
		}
	}
	resp, err := helper.Client(t).Batch.BatchObjectsDelete(
		batch.NewBatchObjectsDeleteParams().WithBody(&models.BatchDelete{
			DryRun:     &dryRun,
			Output:     &output,
			MaxMatches: maxMatches,
			Match: &models.BatchDeleteMatch{
				Class: maxMatchesClassName,
				Where: where,
			},
		}),
		nil,
	)
	if err != nil {
		return nil, err
	}
	require.NotNil(t, resp.Payload.Results)
	return resp.Payload.Results, nil
}

func ptString(s string) *string {
	return &s
}
