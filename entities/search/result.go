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
	"github.com/go-openapi/strfmt"
	"github.com/weaviate/weaviate/entities/models"
)

// Result contains some info of a concept (kind), but not all. For
// additional info the ID can be used to retrieve the full concept from the
// connector storage
type Result struct {
	ID                   strfmt.UUID
	DocID                *uint64
	ClassName            string
	Score                float32
	SecondarySortValue   float32
	ExplainScore         string
	Dist                 float32
	Vector               []float32
	Vectors              models.Vectors
	Beacon               string
	Certainty            float32
	Schema               models.PropertySchema
	Created              int64
	Updated              int64
	AdditionalProperties models.AdditionalProperties
	VectorWeights        map[string]string
	IsConsistent         bool
	Tenant               string
	// Vectors              map[string][]float32

	// Dimensions in case search was vector-based, 0 otherwise
	Dims int
}

type Results []Result

func (r Result) Object() *models.Object {
	return r.ObjectWithVector(true)
}

func (r Result) ObjectWithVector(includeVector bool) *models.Object {
	return r.ObjectWithVectorSelection(includeVector, nil)
}

// ObjectWithVectorSelection builds the API object, controlling which vectors
// are attached:
//   - When includeVector is true, the legacy (unnamed) vector and every target
//     vector are returned ("?include=vector").
//   - When includeVector is false and targetVectors is non-empty, only the
//     named target vectors in targetVectors are returned ("?include=
//     vector:<name>"); a target the object does not carry yet is omitted
//     rather than emitted as nil.
//   - Otherwise neither vector field is set.
func (r Result) ObjectWithVectorSelection(includeVector bool, targetVectors []string) *models.Object {
	schema, ok := r.Schema.(map[string]interface{})
	if ok {
		delete(schema, "id")
	}

	t := &models.Object{
		Class:              r.ClassName,
		ID:                 r.ID,
		Properties:         schema,
		CreationTimeUnix:   r.Created,
		LastUpdateTimeUnix: r.Updated,
		VectorWeights:      r.VectorWeights,
		Tenant:             r.Tenant,
	}

	if r.AdditionalProperties != nil {
		t.Additional = r.AdditionalProperties
	}

	switch {
	case includeVector:
		t.Vector = r.Vector
		t.Vectors = r.Vectors
	case len(targetVectors) > 0 && len(r.Vectors) > 0:
		filtered := make(models.Vectors, len(targetVectors))
		for _, name := range targetVectors {
			if vector, ok := r.Vectors[name]; ok {
				filtered[name] = vector
			}
		}
		if len(filtered) > 0 {
			t.Vectors = filtered
		}
	}

	return t
}

func (rs Results) Objects() []*models.Object {
	return rs.ObjectsWithVector(true)
}

func (rs Results) ObjectsWithVector(includeVector bool) []*models.Object {
	return rs.ObjectsWithVectorSelection(includeVector, nil)
}

// ObjectsWithVectorSelection is the multi-result counterpart of
// Result.ObjectWithVectorSelection.
func (rs Results) ObjectsWithVectorSelection(includeVector bool, targetVectors []string) []*models.Object {
	objects := make([]*models.Object, len(rs))
	for i, res := range rs {
		objects[i] = res.ObjectWithVectorSelection(includeVector, targetVectors)
	}

	return objects
}
