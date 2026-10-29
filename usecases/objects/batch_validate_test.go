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

package objects

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/modulecapabilities"
	"github.com/weaviate/weaviate/entities/schema"
	"github.com/weaviate/weaviate/entities/vectorindex/hnsw"
	"github.com/weaviate/weaviate/usecases/auth/authorization/mocks"
	"github.com/weaviate/weaviate/usecases/config"
	"github.com/weaviate/weaviate/usecases/config/runtime"
	"github.com/weaviate/weaviate/usecases/objects/validation"
	"github.com/weaviate/weaviate/usecases/schema/namespacing"
)

type validateFixture struct {
	objectFinder    *fakeObjectFinder
	schemaManager   *fakeSchemaManager
	modulesProvider *fakeModulesProvider
	manager         *BatchManager
}

func newValidateFixture(t *testing.T, autoSchema bool) *validateFixture {
	t.Helper()

	sch := schemaWithValidateClasses()
	objectFinder := &fakeObjectFinder{}
	cfg := &config.WeaviateConfig{
		Config: config.Config{
			AutoSchema: config.AutoSchema{
				Enabled: runtime.NewDynamicValue(autoSchema),
			},
			TrackVectorDimensions: true,
		},
	}
	schemaManager := &fakeSchemaManager{GetSchemaResponse: sch}
	logger, _ := test.NewNullLogger()
	authorizer := mocks.NewMockAuthorizer()
	modulesProvider := getFakeModulesProvider()
	manager := NewBatchManager(objectFinder, modulesProvider, schemaManager, cfg, logger, authorizer, nil,
		NewAutoSchemaManager(schemaManager, objectFinder, cfg, logger, prometheus.NewPedanticRegistry()), namespacing.Disabled)

	return &validateFixture{
		objectFinder:    objectFinder,
		schemaManager:   schemaManager,
		modulesProvider: modulesProvider,
		manager:         manager,
	}
}

func schemaWithValidateClasses() schema.Schema {
	return schema.Schema{
		Objects: &models.Schema{
			Classes: []*models.Class{
				{
					Vectorizer:        config.VectorizerModuleNone,
					Class:             "Foo",
					VectorIndexConfig: hnsw.UserConfig{},
				},
				{
					Vectorizer:        config.VectorizerModuleNone,
					Class:             "Bar",
					VectorIndexConfig: hnsw.UserConfig{},
				},
				{
					Vectorizer:        config.VectorizerModuleNone,
					Class:             "Boom",
					VectorIndexConfig: hnsw.UserConfig{},
				},
				{
					Vectorizer: config.VectorizerModuleNone,
					Class:      "Multi",
					MultiTenancyConfig: &models.MultiTenancyConfig{
						Enabled: true,
					},
					VectorIndexConfig: hnsw.UserConfig{},
				},
			},
		},
	}
}

func Test_BatchManager_ValidateObjects(t *testing.T) {
	ctx := context.Background()

	t.Run("without any objects it returns the same error as a create", func(t *testing.T) {
		f := newValidateFixture(t, false)
		_, err := f.manager.ValidateObjects(ctx, nil, nil, []*string{}, nil)
		assert.Equal(t, errEmptyObjects, err)
	})

	t.Run("valid objects return success rows in order and are never persisted", func(t *testing.T) {
		f := newValidateFixture(t, false)

		// Only the vectorizer runs; BatchPutObjects has no expectation, so a
		// write attempt would fail this test through the mock's panic.
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()

		objects := []*models.Object{
			{Class: "Foo", Vector: []float32{0.1, 0.2}},
			{Class: "Foo", Vector: []float32{0.3, 0.4}},
		}

		res, err := f.manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 2)
		for i := range res {
			assert.Equal(t, i, res[i].OriginalIndex, "rows keep their original index")
			require.NotNil(t, res[i].Object)
			assert.NoError(t, res[i].Err)
			assert.Len(t, res[i].UUID, 36, "an id is generated just like for a create")
			assert.Equal(t, objects[i].Vector, res[i].Object.Vector,
				"vectorization results stay attached to the object")
		}
		assert.Equal(t, 0, f.schemaManager.TenantActivations, "no tenant activation in validate-only")
	})

	t.Run("a missing class fails only its row even with auto-schema enabled", func(t *testing.T) {
		f := newValidateFixture(t, true)
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()

		objects := []*models.Object{
			{Class: "Foo", Vector: []float32{0.1}},
			{Class: "DoesNotExist", Properties: map[string]interface{}{"a": "b"}},
			{Class: "Foo", Vector: []float32{0.2}},
		}

		res, err := f.manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 3)
		assert.NoError(t, res[0].Err)
		require.Error(t, res[1].Err)
		assert.Contains(t, res[1].Err.Error(), "not present in schema")
		assert.NoError(t, res[2].Err)

		// Nothing was created to make the row pass.
		for _, c := range f.schemaManager.GetSchemaResponse.Objects.Classes {
			assert.NotEqual(t, "DoesNotExist", c.Class)
		}
		assert.Equal(t, 0, f.schemaManager.AddPropertyCalls)
	})

	t.Run("an unknown property fails the row without the property being added", func(t *testing.T) {
		f := newValidateFixture(t, true)
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()

		objects := []*models.Object{
			{
				Class:      "Bar",
				Properties: map[string]interface{}{"unknownProp": "value"},
			},
			{Class: "Bar"},
		}

		res, err := f.manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 2)
		require.Error(t, res[0].Err)
		assert.Contains(t, res[0].Err.Error(), "no such prop with name")
		assert.NoError(t, res[1].Err)
		assert.Equal(t, 0, f.schemaManager.AddPropertyCalls, "auto-schema must not add properties")
	})

	t.Run("invalid input is isolated per row", func(t *testing.T) {
		f := newValidateFixture(t, false)
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()

		objects := []*models.Object{
			{Class: "Foo", Vector: []float32{0.1}},                  // 0 ok
			{Class: "Bar", Vector: []float32{0.2}},                  // 1 ok
			{Class: "Foo", ID: "not-a-uuid"},                        // 2 bad uuid
			{Class: "", Vector: []float32{0.4}},                     // 3 empty class
			{Class: "Foo", Properties: map[string]interface{}{"x": 1}}, // 4 unknown prop
			nil, // 5 null entry
		}

		res, err := f.manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 6)
		assert.NoError(t, res[0].Err)
		assert.NoError(t, res[1].Err)
		require.Error(t, res[2].Err)
		assert.Contains(t, res[2].Err.Error(), "invalid UUID")
		require.Error(t, res[3].Err)
		assert.Contains(t, res[3].Err.Error(), "empty class")
		assert.Nil(t, res[3].Object, "the empty-class row carries no object")
		require.Error(t, res[4].Err)
		assert.Contains(t, res[4].Err.Error(), "no such prop with name")
		require.Error(t, res[5].Err)
		assert.Equal(t, validation.ErrorMissingObject, res[5].Err.Error())
		assert.Nil(t, res[5].Object)
	})

	t.Run("a batch vectorizer error fails just its collection's rows", func(t *testing.T) {
		// Build the manager with a provider whose batch vectorizer fails one
		// named collection and succeeds for every other one.
		sch := schemaWithValidateClasses()
		objectFinder := &fakeObjectFinder{}
		cfg := &config.WeaviateConfig{
			Config: config.Config{
				AutoSchema:            config.AutoSchema{Enabled: runtime.NewDynamicValue(false)},
				TrackVectorDimensions: true,
			},
		}
		schemaManager := &fakeSchemaManager{GetSchemaResponse: sch}
		logger, _ := test.NewNullLogger()
		mp := &batchValidateVectorizer{
			fakeModulesProvider: getFakeModulesProvider(),
			errFor:              map[string]error{"Boom": errors.New("vectorizer unavailable")},
		}
		manager := NewBatchManager(objectFinder, mp, schemaManager, cfg, logger, mocks.NewMockAuthorizer(),
			nil, NewAutoSchemaManager(schemaManager, objectFinder, cfg, logger, prometheus.NewPedanticRegistry()),
			namespacing.Disabled)

		objects := []*models.Object{
			{Class: "Boom"},
			{Class: "Bar"},
			{Class: "Boom"},
		}

		res, err := manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 3)
		require.Error(t, res[0].Err)
		assert.Contains(t, res[0].Err.Error(), "vectorizer unavailable")
		assert.NoError(t, res[1].Err, "another collection vectorizes independently")
		require.Error(t, res[2].Err)
		assert.Contains(t, res[2].Err.Error(), "vectorizer unavailable")
	})

	t.Run("an MT object is validated without creating or activating the tenant", func(t *testing.T) {
		f := newValidateFixture(t, true)
		f.modulesProvider.On("BatchUpdateVector").Return(nil, nil).Once()

		objects := []*models.Object{
			{Class: "Multi", Tenant: "tenantA"},
		}

		res, err := f.manager.ValidateObjects(ctx, nil, objects, []*string{}, nil)
		require.NoError(t, err)
		require.Len(t, res, 1)
		assert.NoError(t, res[0].Err)
		assert.False(t, f.schemaManager.tenantsEnabled, "autoTenants must not run in validate-only")
		assert.Equal(t, 0, f.schemaManager.TenantActivations, "tenants are not activated for validate-only")
	})
}

// batchValidateVectorizer wraps the standard fake so a single collection's
// batch vectorization can fail while all others succeed.
type batchValidateVectorizer struct {
	*fakeModulesProvider
	errFor map[string]error
}

func (p *batchValidateVectorizer) BatchUpdateVector(ctx context.Context, class *models.Class,
	objects []*models.Object, findObjectFn modulecapabilities.FindObjectFn,
	logger logrus.FieldLogger,
) (map[int]error, error) {
	if err := p.errFor[class.Class]; err != nil {
		return nil, err
	}
	return nil, nil
}
