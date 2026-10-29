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
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-openapi/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/adapters/handlers/rest/operations/backups"
	entbackup "github.com/weaviate/weaviate/entities/backup"
	"github.com/weaviate/weaviate/entities/models"
	authzerrors "github.com/weaviate/weaviate/usecases/auth/authorization/errors"
	"github.com/weaviate/weaviate/usecases/auth/authorization/rbac/rbacconf"
	ubak "github.com/weaviate/weaviate/usecases/backup"
	"github.com/weaviate/weaviate/usecases/license"
)

func TestCompressionBackupCfg(t *testing.T) {
	tcs := map[string]struct {
		cfg                 *models.BackupConfig
		expectedCompression ubak.CompressionLevel
		expectedCPU         int
		expectedBucket      string
		expectedPath        string
	}{
		"without config": {
			cfg:                 nil,
			expectedCompression: ubak.GzipDefaultCompression,
			expectedCPU:         ubak.DefaultCPUPercentage,
		},
		"with config": {
			cfg: &models.BackupConfig{
				CPUPercentage:    25,
				CompressionLevel: models.BackupConfigCompressionLevelBestSpeed,
			},
			expectedCompression: ubak.GzipBestSpeed,
			expectedCPU:         25,
		},
		"with partial config [CPU]": {
			cfg: &models.BackupConfig{
				CPUPercentage: 25,
			},
			expectedCompression: ubak.GzipDefaultCompression,
			expectedCPU:         25,
		},
		"with partial config [Compression]": {
			cfg: &models.BackupConfig{
				CompressionLevel: models.BackupConfigCompressionLevelBestSpeed,
			},
			expectedCompression: ubak.GzipBestSpeed,
			expectedCPU:         ubak.DefaultCPUPercentage,
		},
		"with partial config [Bucket]": {
			cfg: &models.BackupConfig{
				Bucket: "a bucket name",
			},
			expectedCompression: ubak.GzipDefaultCompression,
			expectedCPU:         ubak.DefaultCPUPercentage,
			expectedBucket:      "a bucket name",
		},
		"with partial config [Path]": {
			cfg: &models.BackupConfig{
				Path: "a path",
			},
			expectedCompression: ubak.GzipDefaultCompression,
			expectedCPU:         ubak.DefaultCPUPercentage,
			expectedPath:        "a path",
		},
	}

	for n, tc := range tcs {
		t.Run(n, func(t *testing.T) {
			ccfg := compressionFromBCfg(tc.cfg)
			assert.Equal(t, tc.expectedCompression, ccfg.Level)
			assert.Equal(t, tc.expectedCPU, ccfg.CPUPercentage)
		})
	}
}

func TestCompressionRestoreCfg(t *testing.T) {
	tcs := map[string]struct {
		cfg                 *models.RestoreConfig
		expectedCompression ubak.CompressionLevel
		expectedCPU         int
	}{
		"without config": {
			cfg:                 nil,
			expectedCompression: ubak.GzipDefaultCompression,
			expectedCPU:         ubak.DefaultCPUPercentage,
		},
		"with config": {
			cfg: &models.RestoreConfig{
				CPUPercentage: 25,
			},
			expectedCPU: 25,
		},
	}

	for n, tc := range tcs {
		t.Run(n, func(t *testing.T) {
			ccfg := compressionFromRCfg(tc.cfg)
			assert.Equal(t, tc.expectedCPU, ccfg.CPUPercentage)
		})
	}
}

// TestIsRequestFromRootUser verifies that the base backup ID gate passed to the
// list manager is only set for root users (by username or group membership).
func TestIsRequestFromRootUser(t *testing.T) {
	h := &backupHandlers{
		rbacConfig: rbacconf.Config{
			RootUsers:  []string{"root-user"},
			RootGroups: []string{"root-group"},
		},
	}

	tcs := map[string]struct {
		principal *models.Principal
		expectGet bool
	}{
		"root user":            {principal: &models.Principal{Username: "root-user"}, expectGet: true},
		"member of root group": {principal: &models.Principal{Username: "alice", Groups: []string{"root-group"}}, expectGet: true},
		"non-root user":        {principal: &models.Principal{Username: "alice"}, expectGet: false},
		"non-root group":       {principal: &models.Principal{Username: "alice", Groups: []string{"other-group"}}, expectGet: false},
		"nil principal":        {principal: nil, expectGet: false},
	}

	for n, tc := range tcs {
		t.Run(n, func(t *testing.T) {
			assert.Equal(t, tc.expectGet, h.isRequestFromRootUser(tc.principal))
		})
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestParseBackupListPagination(t *testing.T) {
	cases := []struct {
		name       string
		limit      *int64
		cursor     *string
		wantLimit  int
		wantCursor string
		wantErr    bool
	}{
		{name: "no pagination parameters stays unpaginated", wantLimit: 0},
		{name: "limit alone starts the first page", limit: int64Ptr(25), wantLimit: 25},
		{name: "limit and cursor continue paging", limit: int64Ptr(25), cursor: strPtr("token"), wantLimit: 25, wantCursor: "token"},
		{name: "empty cursor is treated as absent", limit: int64Ptr(25), cursor: strPtr(""), wantLimit: 25},
		{name: "cursor without limit is rejected", cursor: strPtr("token"), wantErr: true},
		{name: "zero limit is rejected", limit: int64Ptr(0), wantErr: true},
		{name: "negative limit is rejected", limit: int64Ptr(-3), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, cursor, err := parseBackupListPagination(tc.limit, tc.cursor)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorAs(t, err, &entbackup.ErrUnprocessable{}, "bad paging parameters must map to 422")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantLimit, limit)
			assert.Equal(t, tc.wantCursor, cursor)
		})
	}
}

func TestBackupListCursorWithoutLimitReturns422(t *testing.T) {
	// The validation fails before the manager is touched, so a nil manager
	// proves the error path never reaches listing logic.
	h := &backupHandlers{metricRequestsTotal: newBackupRequestsTotal(nil, nil)}

	responder := h.list(backups.BackupsListParams{
		Backend: "s3",
		Order:   strPtr("asc"),
		Cursor:  strPtr("opaque-token"),
	}, nil)

	unprocessable, ok := responder.(*backups.BackupsListUnprocessableEntity)
	require.True(t, ok, "cursor without limit must be a 422 response, got %T", responder)
	require.NotNil(t, unprocessable.Payload)
	require.Len(t, unprocessable.Payload.Error, 1)
	assert.Contains(t, unprocessable.Payload.Error[0].Message, "limit")
}

func TestBackupsListOKCursorHeader(t *testing.T) {
	t.Run("written when there is a next page", func(t *testing.T) {
		rec := httptest.NewRecorder()
		backups.NewBackupsListOK().
			WithPayload(models.BackupListResponse{}).
			WithXNextCursor("opaque-token").
			WriteResponse(rec, runtime.JSONProducer())

		assert.Equal(t, 200, rec.Code)
		assert.Equal(t, "opaque-token", rec.Header().Get("X-Next-Cursor"))
		assert.Equal(t, "[]", strings.TrimSpace(rec.Body.String()))
	})

	t.Run("absent on the last page and for unpaginated requests", func(t *testing.T) {
		rec := httptest.NewRecorder()
		backups.NewBackupsListOK().
			WithPayload(models.BackupListResponse{}).
			WriteResponse(rec, runtime.JSONProducer())

		assert.Empty(t, rec.Header().Get("X-Next-Cursor"))
	})
}

func TestBackupCreateErrPayload(t *testing.T) {
	refusal := license.Required(ubak.DedupeFeature)
	docs := ", see " + license.EnterpriseDocsURL
	plain := errors.New("no backup backend")
	denied := authzerrors.NewForbidden(nil, "create", "backups/Class-A")
	cases := []struct {
		name      string
		principal *models.Principal
		err       error
		want      string
	}{
		{name: "license refusal names the docs", err: refusal, want: refusal.Error() + docs},
		{name: "wrapped license refusal names the docs", err: fmt.Errorf("backup b1 %w", refusal), want: "backup b1 " + refusal.Error() + docs},
		{
			name:      "a namespace named like the URL scheme keeps the URL whole",
			principal: &models.Principal{Username: "u", Namespace: "https"},
			err:       refusal,
			want:      refusal.Error() + docs,
		},
		{name: "authorization refusal stays as is", err: denied, want: denied.Error()},
		{name: "other error stays as is", err: plain, want: plain.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := backupCreateErrPayload(tc.principal, tc.err)

			require.Len(t, payload.Error, 1)
			require.Equal(t, tc.want, payload.Error[0].Message)
		})
	}
}
