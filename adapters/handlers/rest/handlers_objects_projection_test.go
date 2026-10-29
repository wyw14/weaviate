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
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/weaviate/weaviate/adapters/handlers/rest/operations/objects"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireErrorMessage(t *testing.T, payload *models.ErrorResponse) string {
	t.Helper()
	require.NotNil(t, payload)
	require.Len(t, payload.Error, 1)
	return payload.Error[0].Message
}

func projectionTestClass() *models.Class {
	return &models.Class{
		Class: "MyClass",
		Properties: []*models.Property{
			{Name: "name"},
			{Name: "age"},
			{Name: "nested"},
			{Name: "tags"},
			{Name: "ref"},
		},
	}
}

func projectionTestObject() *models.Object {
	return &models.Object{
		ID:     "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		Class:  "MyClass",
		Tenant: "tenantA",
		Properties: map[string]interface{}{
			"name": "hello world",
			"age":  42,
			"nested": map[string]interface{}{
				"a": 1,
				"b": map[string]interface{}{"deep": true},
			},
			"tags": []interface{}{"x", "y"},
			"ref": models.MultipleRef{
				&models.SingleRef{Beacon: "weaviate://localhost/85f78e29-5937-4390-a121-5379f262b4e5"},
			},
			"secret": "do-not-return",
		},
	}
}

func TestPropertyProjectionParam(t *testing.T) {
	t.Run("absent parameter is disabled", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/objects/MyClass/123", nil)
		assert.Nil(t, propertyProjectionParam(req, nil))
	})

	t.Run("explicit empty value enables projection with empty string", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/objects/MyClass/123?properties=", nil)
		raw := propertyProjectionParam(req, nil)
		require.NotNil(t, raw)
		assert.Equal(t, "", *raw)
	})

	t.Run("parameter without value also enables projection", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/objects/MyClass/123?properties", nil)
		raw := propertyProjectionParam(req, nil)
		require.NotNil(t, raw)
		assert.Equal(t, "", *raw)
	})

	t.Run("values are passed through", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/objects/MyClass/123?properties=name,age", nil)
		raw := propertyProjectionParam(req, nil)
		require.NotNil(t, raw)
		assert.Equal(t, "name,age", *raw)
	})

	t.Run("falls back to bound field without an HTTP request", func(t *testing.T) {
		bound := "name"
		assert.Equal(t, &bound, propertyProjectionParam(nil, &bound))
		assert.Nil(t, propertyProjectionParam(nil, nil))
	})
}

func TestParsePropertyProjection(t *testing.T) {
	t.Run("unset and empty semantics", func(t *testing.T) {
		// an explicit "properties=" selects no properties, but is still enabled
		names, err := parsePropertyProjection("")
		require.NoError(t, err)
		assert.Equal(t, []string{}, names)
	})

	t.Run("comma separated list with trimming", func(t *testing.T) {
		names, err := parsePropertyProjection("name, age ,age")
		require.NoError(t, err)
		assert.Equal(t, []string{"name", "age"}, names)
	})

	t.Run("deduplication preserves first occurrence order", func(t *testing.T) {
		names, err := parsePropertyProjection("tags,name,tags,name")
		require.NoError(t, err)
		assert.Equal(t, []string{"tags", "name"}, names)
	})

	t.Run("empty segment is rejected", func(t *testing.T) {
		_, err := parsePropertyProjection("name,,age")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "position 2")
	})
}

func TestValidateProjectedProperties(t *testing.T) {
	class := projectionTestClass()

	t.Run("known properties pass", func(t *testing.T) {
		assert.NoError(t, validateProjectedProperties(class, []string{"name", "nested"}))
	})

	t.Run("empty whitelist passes", func(t *testing.T) {
		assert.NoError(t, validateProjectedProperties(class, []string{}))
	})

	t.Run("unknown property lists offending and valid names", func(t *testing.T) {
		err := validateProjectedProperties(class, []string{"name", "nope"})
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "'nope'")
		assert.Contains(t, msg, "MyClass")
		assert.Contains(t, msg, "name")
		assert.Contains(t, msg, "nested")
	})

	t.Run("nil class is rejected", func(t *testing.T) {
		assert.Error(t, validateProjectedProperties(nil, []string{"name"}))
	})
}

func TestProjectObjectProperties(t *testing.T) {
	t.Run("keeps only whitelisted top level values", func(t *testing.T) {
		obj := projectionTestObject()
		originalSchema := obj.Properties.(map[string]interface{})
		originalNested := originalSchema["nested"]
		originalTags := originalSchema["tags"]

		projectObjectProperties(obj, []string{"name", "nested", "tags"})

		projected, ok := obj.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Len(t, projected, 3)
		assert.Contains(t, projected, "name")
		assert.Contains(t, projected, "nested")
		assert.Contains(t, projected, "tags")
		assert.NotContains(t, projected, "age")
		assert.NotContains(t, projected, "secret")

		// nested objects and arrays are kept as the complete original value
		assert.Equal(t,
			reflect.ValueOf(originalNested).Pointer(),
			reflect.ValueOf(projected["nested"]).Pointer())
		assert.Equal(t,
			reflect.ValueOf(originalTags).Pointer(),
			reflect.ValueOf(projected["tags"]).Pointer())

		// projection must not touch id, tenant or class
		assert.Equal(t, "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288", string(obj.ID))
		assert.Equal(t, "tenantA", obj.Tenant)
		assert.Equal(t, "MyClass", obj.Class)

		// the original schema map is left untouched
		assert.Len(t, originalSchema, 6)
	})

	t.Run("empty whitelist yields an empty present properties object", func(t *testing.T) {
		obj := projectionTestObject()
		projectObjectProperties(obj, []string{})
		projected, ok := obj.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Empty(t, projected)
	})

	t.Run("missing keys on the object are skipped", func(t *testing.T) {
		obj := &models.Object{Class: "MyClass", Properties: map[string]interface{}{"name": "x"}}
		projectObjectProperties(obj, []string{"name", "age"})
		projected := obj.Properties.(map[string]interface{})
		assert.Len(t, projected, 1)
		assert.Contains(t, projected, "name")
	})

	t.Run("nil properties become an empty object", func(t *testing.T) {
		obj := &models.Object{Class: "MyClass", Properties: nil}
		projectObjectProperties(obj, []string{"name"})
		projected, ok := obj.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Empty(t, projected)
	})
}

func TestGetObjectPropertyProjection(t *testing.T) {
	const cls = "MyClass"

	newHandler := func(obj *models.Object) (*objectHandlers, *fakeManager) {
		fm := &fakeManager{
			getObjectReturn:              obj,
			getObjectClassFromNameReturn: projectionTestClass(),
		}
		return &objectHandlers{
			manager:             fm,
			logger:              &logrus.Logger{},
			metricRequestsTotal: &fakeMetricRequestsTotal{},
		}, fm
	}

	t.Run("projects top level properties and keeps nested values whole", func(t *testing.T) {
		h, fm := newHandler(projectionTestObject())
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f?properties=name,nested,tags", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetOK)
		require.True(t, ok)
		require.NotNil(t, fm.getObjectAdditional)
		assert.False(t, fm.getObjectAdditional.Vector)

		schema, ok := res.Payload.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Len(t, schema, 3)
		assert.Contains(t, schema, "name")
		assert.Contains(t, schema, "nested")
		assert.Contains(t, schema, "tags")
		assert.NotContains(t, schema, "secret")
		assert.Equal(t, "hello world", schema["name"])
		assert.Equal(t, "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288", string(res.Payload.ID))
		assert.Equal(t, "tenantA", res.Payload.Tenant)
	})

	t.Run("explicit empty list returns empty properties", func(t *testing.T) {
		h, _ := newHandler(projectionTestObject())
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f?properties=", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetOK)
		require.True(t, ok)
		schema, ok := res.Payload.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Empty(t, schema)
		assert.Equal(t, "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288", string(res.Payload.ID))

		// the empty projection must serialize as an explicit, present object
		encoded, err := json.Marshal(res.Payload)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"properties":{}`)
	})

	t.Run("unknown property returns a bad request error", func(t *testing.T) {
		h, fm := newHandler(projectionTestObject())
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f?properties=name,unknown", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetBadRequest)
		require.True(t, ok)
		assert.Contains(t, requireErrorMessage(t, res.Payload), "'unknown'")
		assert.Nil(t, fm.getObjectAdditional, "the object must not be fetched when projection validation fails")
	})

	t.Run("malformed projection returns a bad request error", func(t *testing.T) {
		h, _ := newHandler(projectionTestObject())
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f?properties=name,,age", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetBadRequest)
		require.True(t, ok)
		assert.Contains(t, requireErrorMessage(t, res.Payload), "invalid 'properties' parameter")
	})

	t.Run("projection without collection is a bad request", func(t *testing.T) {
		h, _ := newHandler(projectionTestObject())
		params := objects.ObjectsGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/90608b1f?properties=name", nil),
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObjectDeprecated(params, nil).(*objects.ObjectsClassGetBadRequest)
		require.True(t, ok)
		assert.Contains(t, requireErrorMessage(t, res.Payload), "requires the collection")
	})

	t.Run("without the parameter all properties are returned", func(t *testing.T) {
		h, fm := newHandler(projectionTestObject())
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetOK)
		require.True(t, ok)
		schema, ok := res.Payload.Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Len(t, schema, 6)
		assert.Zero(t, fm.getObjectClassFromNameCalls, "no schema lookup is needed without include/projection")
	})

	t.Run("projection does not interfere with the include option", func(t *testing.T) {
		obj := projectionTestObject()
		obj.Additional = models.AdditionalProperties{"vector": []float32{1, 2, 3}}
		h, fm := newHandler(obj)
		include := "vector"
		params := objects.ObjectsClassGetParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects/MyClass/90608b1f?include=vector&properties=name", nil),
			ClassName:   cls,
			ID:          "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288",
			Include:     &include,
		}

		res, ok := h.getObject(params, nil).(*objects.ObjectsClassGetOK)
		require.True(t, ok)
		require.NotNil(t, fm.getObjectAdditional)
		assert.True(t, fm.getObjectAdditional.Vector, "include=vector must still be passed to the manager")
		assert.Equal(t, models.AdditionalProperties{"vector": []float32{1, 2, 3}}, res.Payload.Additional)
		schema := res.Payload.Properties.(map[string]interface{})
		assert.Len(t, schema, 1)
		assert.Contains(t, schema, "name")
	})
}

func TestGetObjectsPropertyProjection(t *testing.T) {
	className := "MyClass"

	t.Run("projects every listed object", func(t *testing.T) {
		fm := &fakeManager{
			queryResult:                  []*models.Object{projectionTestObject()},
			getObjectClassFromNameReturn: projectionTestClass(),
		}
		h := &objectHandlers{manager: fm, metricRequestsTotal: &fakeMetricRequestsTotal{}}
		params := objects.ObjectsListParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects?class=MyClass&properties=name,nested", nil),
			Class:       &className,
		}

		res, ok := h.getObjects(params, nil).(*objects.ObjectsListOK)
		require.True(t, ok)
		require.Len(t, res.Payload.Objects, 1)
		schema, ok := res.Payload.Objects[0].Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Len(t, schema, 2)
		assert.Contains(t, schema, "name")
		assert.Contains(t, schema, "nested")
		assert.NotContains(t, schema, "secret")
		assert.Equal(t, "90608b1f-9ed0-4c08-8aa1-9f8c21d2f288", string(res.Payload.Objects[0].ID))
		assert.Equal(t, "tenantA", res.Payload.Objects[0].Tenant)
	})

	t.Run("explicit empty list returns empty properties for every object", func(t *testing.T) {
		fm := &fakeManager{
			queryResult:                  []*models.Object{projectionTestObject()},
			getObjectClassFromNameReturn: projectionTestClass(),
		}
		h := &objectHandlers{manager: fm, metricRequestsTotal: &fakeMetricRequestsTotal{}}
		params := objects.ObjectsListParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects?class=MyClass&properties=", nil),
			Class:       &className,
		}

		res, ok := h.getObjects(params, nil).(*objects.ObjectsListOK)
		require.True(t, ok)
		schema, ok := res.Payload.Objects[0].Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Empty(t, schema)
	})

	t.Run("projection requires the class parameter", func(t *testing.T) {
		fm := &fakeManager{}
		h := &objectHandlers{manager: fm, metricRequestsTotal: &fakeMetricRequestsTotal{}}
		params := objects.ObjectsListParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects?properties=name", nil),
		}

		res, ok := h.getObjects(params, nil).(*objects.ObjectsListBadRequest)
		require.True(t, ok)
		assert.Contains(t, requireErrorMessage(t, res.Payload), "requires the 'class' parameter")
	})

	t.Run("unknown property returns a bad request error", func(t *testing.T) {
		fm := &fakeManager{
			queryResult:                  []*models.Object{projectionTestObject()},
			getObjectClassFromNameReturn: projectionTestClass(),
		}
		h := &objectHandlers{manager: fm, metricRequestsTotal: &fakeMetricRequestsTotal{}}
		params := objects.ObjectsListParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects?class=MyClass&properties=nope", nil),
			Class:       &className,
		}

		res, ok := h.getObjects(params, nil).(*objects.ObjectsListBadRequest)
		require.True(t, ok)
		assert.Contains(t, requireErrorMessage(t, res.Payload), "'nope'")
	})

	t.Run("no parameter keeps returning all properties", func(t *testing.T) {
		fm := &fakeManager{
			queryResult:                  []*models.Object{projectionTestObject()},
			getObjectClassFromNameReturn: projectionTestClass(),
		}
		h := &objectHandlers{manager: fm, metricRequestsTotal: &fakeMetricRequestsTotal{}}
		params := objects.ObjectsListParams{
			HTTPRequest: httptest.NewRequest("GET", "/v1/objects?class=MyClass", nil),
			Class:       &className,
		}

		res, ok := h.getObjects(params, nil).(*objects.ObjectsListOK)
		require.True(t, ok)
		schema, ok := res.Payload.Objects[0].Properties.(map[string]interface{})
		require.True(t, ok)
		assert.Len(t, schema, 6)
	})
}
