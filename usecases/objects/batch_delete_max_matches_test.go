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
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/mock"

	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/entities/schema"
	"github.com/weaviate/weaviate/entities/vectorindex/hnsw"
	"github.com/weaviate/weaviate/entities/verbosity"
	"github.com/weaviate/weaviate/usecases/auth/authorization/mocks"
	"github.com/weaviate/weaviate/usecases/config"
	"github.com/weaviate/weaviate/usecases/config/runtime"
	"github.com/weaviate/weaviate/usecases/schema/namespacing"
)

func Test_BatchDelete_MaxMatchesValidation(t *testing.T) {
	const queryMaximumResults = int64(10)

	makeManager := func(t *testing.T, limit int64) (*BatchManager, *fakeObjectFinder) {
		t.Helper()
		sch := schema.Schema{Objects: &models.Schema{Classes: []*models.Class{{
			Class: "Foo",
			Properties: []*models.Property{{
				Name:         "name",
				DataType:     schema.DataTypeText.PropString(),
				Tokenization:  models.PropertyTokenizationWhitespace,
			}},
			VectorIndexConfig: hnsw.UserConfig{},
			Vectorizer:        config.VectorizerModuleNone,
		}}}}
		cfg := &config.WeaviateConfig{Config: config.Config{
			AutoSchema:          config.AutoSchema{Enabled: runtime.NewDynamicValue(false)},
			QueryMaximumResults: limit,
		}}
		schemaManager := &fakeSchemaManager{GetSchemaResponse: sch}
		logger, _ := test.NewNullLogger()
		vectorRepo := &fakeObjectFinder{}
		manager := NewBatchManager(vectorRepo, getFakeModulesProvider(), schemaManager, cfg, logger,
			mocks.NewMockAuthorizer(), nil,
			NewAutoSchemaManager(schemaManager, vectorRepo, cfg, logger, prometheus.NewPedanticRegistry()),
			namespacing.Disabled)
		return manager, vectorRepo
	}

	newMatch := func() *models.BatchDeleteMatch {
		return &models.BatchDeleteMatch{
			Class: "Foo",
			Where: &models.WhereFilter{
				Path:      []string{"name"},
				Operator:  "Equal",
				ValueText: ptString("v"),
			},
		}
	}

	ctx := context.Background()
	principal := &models.Principal{Username: "admin"}

	t.Run("a negative threshold is invalid user input and never reaches the repo", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, queryMaximumResults)

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), batchDeleteMaxMatchesPtr(-1), nil, "")
		require.Error(t, err)
		var invalid ErrInvalidUserInput
		require.True(t, errors.As(err, &invalid), "got %T: %v", err, err)
		require.ErrorContains(t, err, "maxMatches")
		vectorRepo.AssertNotCalled(t, "BatchDeleteObjects", mock.Anything)
	})

	t.Run("a threshold above the positive global limit is rejected before the repo", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, queryMaximumResults)

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), batchDeleteMaxMatchesPtr(queryMaximumResults+1), nil, "")
		require.Error(t, err)
		var invalid ErrInvalidUserInput
		require.True(t, errors.As(err, &invalid), "got %T: %v", err, err)
		require.ErrorContains(t, err, "QUERY_MAXIMUM_RESULTS")
		vectorRepo.AssertNotCalled(t, "BatchDeleteObjects", mock.Anything)
	})

	t.Run("a threshold equal to the positive global limit reaches the repo", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, queryMaximumResults)
		var captured BatchDeleteParams
		vectorRepo.On("BatchDeleteObjects", mock.MatchedBy(func(p BatchDeleteParams) bool {
			captured = p
			return true
		})).Return(BatchDeleteResult{}, nil).Once()

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), batchDeleteMaxMatchesPtr(queryMaximumResults), nil, "")
		require.NoError(t, err)
		require.NotNil(t, captured.MaxMatches)
		require.Equal(t, queryMaximumResults, *captured.MaxMatches)
		vectorRepo.AssertExpectations(t)
	})

	t.Run("an explicit zero reaches the repo as a set zero, not as disabled", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, queryMaximumResults)
		var captured BatchDeleteParams
		vectorRepo.On("BatchDeleteObjects", mock.MatchedBy(func(p BatchDeleteParams) bool {
			captured = p
			return true
		})).Return(BatchDeleteResult{}, nil).Once()

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), batchDeleteMaxMatchesPtr(0), nil, "")
		require.NoError(t, err)
		require.NotNil(t, captured.MaxMatches, "0 is an enabled threshold")
		require.Zero(t, *captured.MaxMatches)
		vectorRepo.AssertExpectations(t)
	})

	t.Run("an omitted threshold reaches the repo as nil", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, queryMaximumResults)
		var captured BatchDeleteParams
		vectorRepo.On("BatchDeleteObjects", mock.MatchedBy(func(p BatchDeleteParams) bool {
			captured = p
			return true
		})).Return(BatchDeleteResult{}, nil).Once()

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), nil, nil, "")
		require.NoError(t, err)
		require.Nil(t, captured.MaxMatches, "nil disables the protection")
		vectorRepo.AssertExpectations(t)
	})

	t.Run("a threshold with the global limit disabled is allowed", func(t *testing.T) {
		manager, vectorRepo := makeManager(t, 0)
		vectorRepo.On("BatchDeleteObjects", mock.MatchedBy(func(p BatchDeleteParams) bool {
			return p.MaxMatches != nil && *p.MaxMatches == 5
		})).Return(BatchDeleteResult{}, nil).Once()

		_, err := manager.DeleteObjects(ctx, principal, newMatch(), nil, ptBool(false),
			ptString(verbosity.OutputMinimal), batchDeleteMaxMatchesPtr(5), nil, "")
		require.NoError(t, err)
		vectorRepo.AssertExpectations(t)
	})
}

func batchDeleteMaxMatchesPtr(n int64) *int64 {
	return &n
}
