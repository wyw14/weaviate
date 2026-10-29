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

package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/require"

	clobjects "github.com/weaviate/weaviate/client/objects"
	clschema "github.com/weaviate/weaviate/client/schema"
	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/schema"
	"github.com/weaviate/weaviate/test/docker"
	"github.com/weaviate/weaviate/test/helper"
)

// TestObjectIncludeTargetVector covers ?include=vector:<target-name> on the
// REST object read endpoints: only the requested target vector is returned
// (and transmitted), unknown/empty target names are rejected, bare
// include=vector still returns all vectors and takes precedence, and targets
// the object does not carry yet stay absent.
func TestObjectIncludeTargetVector(t *testing.T) {
	ctx := context.Background()
	compose, err := docker.New().
		WithWeaviate().
		Start(ctx)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, compose.Terminate(ctx))
	}()

	helper.SetupClient(compose.GetWeaviate().URI())
	defer helper.ResetClient()

	const (
		className   = "IncludeTargetVector"
		legacyClass = "IncludeTargetVectorLegacy"
		vecA        = "vecA"
		vecB        = "vecB"
		dimA        = 3
		dimB        = 4
	)

	mkVec := func(dim, seed int) []float32 {
		v := make([]float32, dim)
		for i := range v {
			v[i] = float32(seed) + float32(i)*0.01
		}
		return v
	}

	t.Cleanup(func() {
		_, _ = helper.Client(t).Schema.SchemaObjectsDelete(
			clschema.NewSchemaObjectsDeleteParams().WithClassName(className), nil)
		_, _ = helper.Client(t).Schema.SchemaObjectsDelete(
			clschema.NewSchemaObjectsDeleteParams().WithClassName(legacyClass), nil)
	})

	_, err = helper.Client(t).Schema.SchemaObjectsCreate(
		clschema.NewSchemaObjectsCreateParams().WithObjectClass(&models.Class{
			Class: className,
			Properties: []*models.Property{
				{Name: "name", DataType: []string{schema.DataTypeText.String()}},
			},
			VectorConfig: map[string]models.VectorConfig{
				vecA: {
					Vectorizer:      map[string]any{"none": map[string]any{}},
					VectorIndexType: "hnsw",
				},
				vecB: {
					Vectorizer:      map[string]any{"none": map[string]any{}},
					VectorIndexType: "hnsw",
				},
			},
		}), nil)
	require.NoError(t, err)

	_, err = helper.Client(t).Schema.SchemaObjectsCreate(
		clschema.NewSchemaObjectsCreateParams().WithObjectClass(&models.Class{
			Class:      legacyClass,
			Vectorizer: "none",
			Properties: []*models.Property{
				{Name: "name", DataType: []string{schema.DataTypeText.String()}},
			},
		}), nil)
	require.NoError(t, err)

	fullID := strfmt.UUID("00000000-0000-0000-0000-000000000a01")
	_, err = helper.Client(t).Objects.ObjectsCreate(
		clobjects.NewObjectsCreateParams().WithBody(&models.Object{
			ID:    fullID,
			Class: className,
			Properties: map[string]any{
				"name": "carries both targets",
			},
			Vectors: models.Vectors{
				vecA: mkVec(dimA, 1),
				vecB: mkVec(dimB, 2),
			},
		}), nil)
	require.NoError(t, err)

	// An object that has not been vectorized for vecB yet.
	partialID := strfmt.UUID("00000000-0000-0000-0000-000000000a02")
	_, err = helper.Client(t).Objects.ObjectsCreate(
		clobjects.NewObjectsCreateParams().WithBody(&models.Object{
			ID:    partialID,
			Class: className,
			Properties: map[string]any{
				"name": "carries only vecA",
			},
			Vectors: models.Vectors{
				vecA: mkVec(dimA, 3),
			},
		}), nil)
	require.NoError(t, err)

	legacyID := strfmt.UUID("00000000-0000-0000-0000-000000000a03")
	_, err = helper.Client(t).Objects.ObjectsCreate(
		clobjects.NewObjectsCreateParams().WithBody(&models.Object{
			ID:         legacyID,
			Class:      legacyClass,
			Properties: map[string]any{"name": "legacy single vector"},
			Vector:     mkVec(2, 9),
		}), nil)
	require.NoError(t, err)

	getObject := func(t *testing.T, include *string) *models.Object {
		t.Helper()
		params := clobjects.NewObjectsClassGetParams().
			WithClassName(className).WithID(fullID)
		if include != nil {
			params = params.WithInclude(include)
		}
		res, err := helper.Client(t).Objects.ObjectsClassGet(params, nil)
		require.NoError(t, err)
		return res.Payload
	}

	t.Run("single named target returns only that target", func(t *testing.T) {
		obj := getObject(t, ptString(fmt.Sprintf("vector:%s", vecA)))
		require.Len(t, obj.Vectors, 1)
		require.Contains(t, obj.Vectors, vecA)
		require.NotContains(t, obj.Vectors, vecB)
		require.Len(t, obj.Vectors[vecA].([]interface{}), dimA)
		require.Nil(t, obj.Vector, "unnamed vector must stay absent")
	})

	t.Run("other target selection", func(t *testing.T) {
		obj := getObject(t, ptString(fmt.Sprintf("vector:%s", vecB)))
		require.Len(t, obj.Vectors, 1)
		require.Contains(t, obj.Vectors, vecB)
		require.Len(t, obj.Vectors[vecB].([]interface{}), dimB)
	})

	t.Run("no include omits the vectors field entirely", func(t *testing.T) {
		obj := getObject(t, nil)
		require.Nil(t, obj.Vectors)
		require.Nil(t, obj.Vector)
	})

	t.Run("bare include=vector returns every target", func(t *testing.T) {
		obj := getObject(t, ptString("vector"))
		require.Len(t, obj.Vectors, 2)
		require.Contains(t, obj.Vectors, vecA)
		require.Contains(t, obj.Vectors, vecB)
	})

	t.Run("include=vector combined with a target selects all vectors", func(t *testing.T) {
		obj := getObject(t, ptString(fmt.Sprintf("vector,vector:%s", vecA)))
		require.Len(t, obj.Vectors, 2)
		require.Contains(t, obj.Vectors, vecA)
		require.Contains(t, obj.Vectors, vecB)
	})

	t.Run("combines with classification include item", func(t *testing.T) {
		obj := getObject(t, ptString(fmt.Sprintf("classification,vector:%s", vecA)))
		require.Len(t, obj.Vectors, 1)
		require.Contains(t, obj.Vectors, vecA)
	})

	t.Run("target the object does not carry yet stays missing", func(t *testing.T) {
		res, err := helper.Client(t).Objects.ObjectsClassGet(
			clobjects.NewObjectsClassGetParams().
				WithClassName(className).WithID(partialID).
				WithInclude(ptString(fmt.Sprintf("vector:%s", vecB))), nil)
		require.NoError(t, err)
		require.Nil(t, res.Payload.Vectors, "absent target must not be emitted as nil entry")

		// requesting the present target still works
		res, err = helper.Client(t).Objects.ObjectsClassGet(
			clobjects.NewObjectsClassGetParams().
				WithClassName(className).WithID(partialID).
				WithInclude(ptString(fmt.Sprintf("vector:%s", vecA))), nil)
		require.NoError(t, err)
		require.Len(t, res.Payload.Vectors, 1)
		require.Contains(t, res.Payload.Vectors, vecA)
	})

	t.Run("unknown target name is a bad request", func(t *testing.T) {
		_, err := helper.Client(t).Objects.ObjectsClassGet(
			clobjects.NewObjectsClassGetParams().
				WithClassName(className).WithID(fullID).
				WithInclude(ptString("vector:doesNotExist")), nil)
		badRequest, ok := err.(*clobjects.ObjectsClassGetBadRequest)
		require.True(t, ok, "expected bad request, got %v", err)
		require.Contains(t, badRequest.Payload.Error[0].Message, "doesNotExist")
	})

	t.Run("empty target name is a bad request", func(t *testing.T) {
		_, err := helper.Client(t).Objects.ObjectsClassGet(
			clobjects.NewObjectsClassGetParams().
				WithClassName(className).WithID(fullID).
				WithInclude(ptString("vector:")), nil)
		_, ok := err.(*clobjects.ObjectsClassGetBadRequest)
		require.True(t, ok, "expected bad request, got %v", err)
	})

	t.Run("target selection on a legacy vector class is a bad request", func(t *testing.T) {
		_, err := helper.Client(t).Objects.ObjectsClassGet(
			clobjects.NewObjectsClassGetParams().
				WithClassName(legacyClass).WithID(legacyID).
				WithInclude(ptString(fmt.Sprintf("vector:%s", vecA))), nil)
		_, ok := err.(*clobjects.ObjectsClassGetBadRequest)
		require.True(t, ok, "expected bad request, got %v", err)
	})

	t.Run("class list with a target selection filters every object", func(t *testing.T) {
		limit := int64(10)
		classQuery := className
		res, err := helper.Client(t).Objects.ObjectsList(
			clobjects.NewObjectsListParams().
				WithClass(&classQuery).
				WithLimit(&limit).
				WithInclude(ptString(fmt.Sprintf("vector:%s", vecA))), nil)
		require.NoError(t, err)
		require.NotEmpty(t, res.Payload.Objects)
		for _, obj := range res.Payload.Objects {
			require.Contains(t, obj.Vectors, vecA)
			require.NotContains(t, obj.Vectors, vecB,
				"targets that were not requested must not be transmitted")
		}
	})

	t.Run("class-less list with a target selection is a bad request", func(t *testing.T) {
		_, err := helper.Client(t).Objects.ObjectsList(
			clobjects.NewObjectsListParams().
				WithInclude(ptString(fmt.Sprintf("vector:%s", vecA))), nil)
		_, ok := err.(*clobjects.ObjectsListBadRequest)
		require.True(t, ok, "expected bad request, got %v", err)
	})
}
