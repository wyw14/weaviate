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

package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/models"
)

func namedVectorsResult() Result {
	return Result{
		Vector: []float32{0.1, 0.2},
		Vectors: models.Vectors{
			"vecA": []float32{1, 2, 3},
			"vecB": []float32{4, 5, 6, 7},
			"colbert": [][]float32{{7, 8}, {9, 10}},
		},
	}
}

func TestResult_ObjectWithVectorSelection(t *testing.T) {
	t.Run("no vectors requested omits both fields", func(t *testing.T) {
		obj := namedVectorsResult().ObjectWithVectorSelection(false, nil)
		assert.Nil(t, obj.Vector)
		assert.Nil(t, obj.Vectors)
	})

	t.Run("full include attaches legacy and all target vectors", func(t *testing.T) {
		obj := namedVectorsResult().ObjectWithVectorSelection(true, nil)
		assert.Equal(t, models.C11yVector{0.1, 0.2}, obj.Vector)
		assert.Len(t, obj.Vectors, 3)
	})

	t.Run("specific target returns only that target", func(t *testing.T) {
		obj := namedVectorsResult().ObjectWithVectorSelection(false, []string{"vecA"})
		assert.Nil(t, obj.Vector, "legacy vector must stay absent")
		require.Len(t, obj.Vectors, 1)
		assert.Contains(t, obj.Vectors, "vecA")
		assert.NotContains(t, obj.Vectors, "vecB")
		assert.NotContains(t, obj.Vectors, "colbert")
	})

	t.Run("several targets preserve the requested order", func(t *testing.T) {
		obj := namedVectorsResult().ObjectWithVectorSelection(false, []string{"colbert", "vecA"})
		assert.Len(t, obj.Vectors, 2)
		assert.Contains(t, obj.Vectors, "colbert")
		assert.Contains(t, obj.Vectors, "vecA")
	})

	t.Run("target the object does not carry yet stays missing", func(t *testing.T) {
		// none of the requested targets exists: vectors field must be omitted
		// rather than emitted with nil entries
		obj := namedVectorsResult().ObjectWithVectorSelection(false, []string{"vecC"})
		assert.Nil(t, obj.Vector)
		assert.Nil(t, obj.Vectors)

		// a partially satisfied selection keeps only present targets
		obj = namedVectorsResult().ObjectWithVectorSelection(false, []string{"vecA", "vecC"})
		require.Len(t, obj.Vectors, 1)
		assert.Contains(t, obj.Vectors, "vecA")
	})

	t.Run("selection on an object without any vectors is a no-op", func(t *testing.T) {
		obj := Result{}.ObjectWithVectorSelection(false, []string{"vecA"})
		assert.Nil(t, obj.Vectors)
	})
}

func TestResults_ObjectsWithVectorSelection(t *testing.T) {
	results := Results{namedVectorsResult(), namedVectorsResult()}
	objects := results.ObjectsWithVectorSelection(false, []string{"vecB"})
	require.Len(t, objects, 2)
	for _, obj := range objects {
		require.Len(t, obj.Vectors, 1)
		assert.Contains(t, obj.Vectors, "vecB")
	}
}
