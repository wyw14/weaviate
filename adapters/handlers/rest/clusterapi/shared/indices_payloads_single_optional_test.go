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

package shared

import (
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/entities/additional"
	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/storobj"
)

func singleObjectWithTargetVectors() *storobj.Object {
	obj := storobj.New(7)
	obj.MarshallerVersion = 1
	obj.Object = models.Object{
		ID:    strfmt.UUID("73f2eb5f-5abf-447a-81ca-74b1dd168247"),
		Class: "MultiVec",
		Properties: map[string]interface{}{
			"title": "hello world",
		},
	}
	obj.Vectors = map[string][]float32{
		"vecA": {1, 2, 3},
		"vecB": {4, 5, 6, 7},
	}
	obj.MultiVectors = map[string][][]float32{
		"colbert": {{7, 8}, {9, 10}},
	}
	return obj
}

func roundTripSingle(t *testing.T, obj *storobj.Object, addProps additional.Properties) *storobj.Object {
	t.Helper()

	payload, err := IndicesPayloads.SingleObject.MarshalWithAdditional(obj, MethodGet, addProps)
	require.NoError(t, err)

	out, err := IndicesPayloads.SingleObject.Unmarshal(payload, MethodGet)
	require.NoError(t, err)
	return out
}

func TestSingleObjectPayload_MarshalWithAdditional(t *testing.T) {
	t.Run("no vectors requested strips every vector", func(t *testing.T) {
		out := roundTripSingle(t, singleObjectWithTargetVectors(), additional.Properties{})
		assert.Empty(t, out.Vector)
		assert.Empty(t, out.Vectors)
		assert.Empty(t, out.MultiVectors)
	})

	t.Run("full vector request returns every target", func(t *testing.T) {
		out := roundTripSingle(t, singleObjectWithTargetVectors(), additional.Properties{
			Vector:                  true,
			IncludeAllTargetVectors: true,
		})
		assert.Len(t, out.Vectors, 2)
		assert.Contains(t, out.Vectors, "vecA")
		assert.Contains(t, out.Vectors, "vecB")
		assert.Len(t, out.MultiVectors, 1)
		assert.Contains(t, out.MultiVectors, "colbert")
	})

	t.Run("specific named target is the only target transmitted", func(t *testing.T) {
		out := roundTripSingle(t, singleObjectWithTargetVectors(), additional.Properties{
			Vectors: []string{"vecA"},
		})
		require.Len(t, out.Vectors, 1)
		assert.Contains(t, out.Vectors, "vecA")
		assert.NotContains(t, out.Vectors, "vecB")
		assert.Empty(t, out.MultiVectors, "targets not requested must not cross the wire")
	})

	t.Run("multi-vector target selection", func(t *testing.T) {
		out := roundTripSingle(t, singleObjectWithTargetVectors(), additional.Properties{
			Vectors: []string{"colbert"},
		})
		assert.Empty(t, out.Vectors)
		require.Len(t, out.MultiVectors, 1)
		assert.Contains(t, out.MultiVectors, "colbert")
	})

	t.Run("target the object does not carry yields no vectors", func(t *testing.T) {
		out := roundTripSingle(t, singleObjectWithTargetVectors(), additional.Properties{
			Vectors: []string{"vecC"},
		})
		assert.Empty(t, out.Vectors)
		assert.Empty(t, out.MultiVectors)
	})

	t.Run("non-GET method is unsupported", func(t *testing.T) {
		_, err := IndicesPayloads.SingleObject.MarshalWithAdditional(
			singleObjectWithTargetVectors(), MethodPut, additional.Properties{})
		require.Error(t, err)
	})
}
