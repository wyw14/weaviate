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

package db

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/require"

	"github.com/weaviate/weaviate/usecases/objects"
)

func TestPerShardResolveLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int64
		want  int
	}{
		{name: "the default", limit: 10000, want: 10001},
		{name: "one", limit: 1, want: 2},
		{name: "zero means no cap", limit: 0, want: 0},
		{name: "negative means no cap", limit: -1, want: 0},
		{name: "the largest value that is not clamped", limit: math.MaxInt32 - 1, want: math.MaxInt32},
		{name: "the first value that is clamped", limit: math.MaxInt32, want: math.MaxInt32},
		{name: "the largest int64", limit: math.MaxInt64, want: math.MaxInt32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := perShardResolveLimit(tt.limit)
			require.Equal(t, tt.want, got)
			require.GreaterOrEqual(t, got, 0, "a negative limit reads as no cap all the way down")
		})
	}
}

func TestPlanShardDeletes(t *testing.T) {
	tests := []struct {
		name string
		// resolved is how many UUIDs each shard returned, shard i being named shard-i.
		resolved         []int
		limit            int64
		wantDeleted      int
		wantShards       int
		wantMatches      int64
		wantCappedShards int
	}{
		{
			name:        "no shard matched",
			resolved:    []int{0, 0, 0},
			limit:       10,
			wantDeleted: 0,
			wantShards:  0,
			wantMatches: 0,
		},
		{
			name:        "fewer matches than the limit",
			resolved:    []int{2, 3},
			limit:       10,
			wantDeleted: 5,
			wantShards:  2,
			wantMatches: 5,
		},
		{
			name:        "exactly as many matches as the limit",
			resolved:    []int{10},
			limit:       10,
			wantDeleted: 10,
			wantShards:  1,
			wantMatches: 10,
		},
		{
			name:             "one match more than the limit",
			resolved:         []int{11},
			limit:            10,
			wantDeleted:      10,
			wantShards:       1,
			wantMatches:      11,
			wantCappedShards: 1,
		},
		{
			name:             "every shard capped",
			resolved:         []int{11, 11, 11},
			limit:            10,
			wantDeleted:      10,
			wantShards:       1,
			wantMatches:      11,
			wantCappedShards: 3,
		},
		{
			name:             "an empty shard first",
			resolved:         []int{0, 11},
			limit:            10,
			wantDeleted:      10,
			wantShards:       1,
			wantMatches:      11,
			wantCappedShards: 1,
		},
		{
			name:        "the delete list is filled from more than one shard",
			resolved:    []int{6, 6, 6},
			limit:       10,
			wantDeleted: 10,
			wantShards:  2,
			wantMatches: 11,
		},
		{
			name:        "a limit of zero deletes nothing",
			resolved:    []int{5},
			limit:       0,
			wantDeleted: 0,
			wantShards:  0,
			wantMatches: 5,
		},
		{
			name:        "a negative limit deletes nothing",
			resolved:    []int{5},
			limit:       -1,
			wantDeleted: 0,
			wantShards:  0,
			wantMatches: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planShardDeletes(shardUUIDs(tt.resolved), tt.limit)

			deleted := 0
			for shardName, uuids := range plan.toDelete {
				require.NotEmpty(t, uuids, "shard %q takes a delete round trip for nothing", shardName)
				deleted += len(uuids)
			}
			require.Equal(t, tt.wantDeleted, deleted)
			require.Len(t, plan.toDelete, tt.wantShards)
			require.Equal(t, tt.wantMatches, plan.matches)
			require.Equal(t, tt.wantCappedShards, plan.cappedShards)
		})
	}
}

// TestMaxMatchesExceededError pins the safety gate on the plan of one resolve:
// the unclamped total across every shard is compared to the threshold, equality
// runs, anything above rejects (whether or not the reply count would have been
// clamped), an explicit zero only admits a zero match, and a shard that filled
// its resolve window turns the number into a lower bound.
func TestMaxMatchesExceededError(t *testing.T) {
	tests := []struct {
		name      string
		resolved  []int
		limit     int64
		maxMatches int64
		dryRun    bool
		wantErr   bool
		// wantMatches and wantAtLeast only matter when wantErr.
		wantMatches int64
		wantAtLeast bool
	}{
		{
			name:       "no match with an explicit zero threshold",
			resolved:   []int{0, 0},
			limit:      10,
			maxMatches: 0,
			wantErr:    false,
		},
		{
			name:       "one shard below the threshold",
			resolved:   []int{4},
			limit:      10,
			maxMatches: 5,
			wantErr:    false,
		},
		{
			name:       "the shard total equals the threshold",
			resolved:   []int{2, 3},
			limit:      10,
			maxMatches: 5,
			wantErr:    false,
		},
		{
			name:        "the shard total is one over the threshold",
			resolved:    []int{2, 4},
			limit:       10,
			maxMatches:  5,
			wantErr:     true,
			wantMatches: 6,
			// No shard filled the per-shard window of 11, so the sum is exact.
			wantAtLeast: false,
		},
		{
			name:        "many shards sum over the threshold while each stays under its window",
			resolved:    []int{4, 4, 4},
			limit:       10,
			maxMatches:  5,
			wantErr:     true,
			wantMatches: 12,
			wantAtLeast: false,
		},
		{
			name:        "explicit zero rejects any match",
			resolved:    []int{1},
			limit:       10,
			maxMatches: 0,
			wantErr:     true,
			wantMatches: 1,
		},
		{
			name:        "explicit zero rejects a multi-shard match",
			resolved:    []int{0, 3, 0},
			limit:       10,
			maxMatches: 0,
			wantErr:     true,
			wantMatches: 3,
		},
		{
			name:        "a capped shard makes the count a lower bound",
			resolved:    []int{11},
			limit:       10,
			maxMatches:  5,
			dryRun:      true,
			wantErr:     true,
			wantMatches: 11,
			wantAtLeast: true,
		},
		{
			name:        "several capped shards sum into a lower bound",
			resolved:    []int{11, 11, 11},
			limit:       10,
			maxMatches:  10,
			wantErr:     true,
			wantMatches: 33,
			wantAtLeast: true,
		},
		{
			name:       "an uncapped resolve equal to the threshold is exact and runs",
			resolved:   []int{5},
			limit:      0,
			maxMatches: 5,
			wantErr:    false,
		},
		{
			name:        "an uncapped resolve over the threshold reports the exact total",
			resolved:    []int{2, 4},
			limit:       -1,
			maxMatches:  5,
			dryRun:      true,
			wantErr:     true,
			wantMatches: 6,
			wantAtLeast: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planShardDeletes(shardUUIDs(tt.resolved), tt.limit)

			var expectedTotal int64
			for _, n := range tt.resolved {
				expectedTotal += int64(n)
			}
			require.Equal(t, expectedTotal, plan.resolvedTotal,
				"the gate reads the unclamped sum, not the reply count clamped to limit+1")

			err := maxMatchesExceededError(plan, tt.maxMatches, tt.dryRun)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			var exceeded objects.ErrBatchDeleteMaxMatchesExceeded
			require.True(t, errors.As(err, &exceeded), "expected ErrBatchDeleteMaxMatchesExceeded, got %T: %v", err, err)
			require.Equal(t, tt.maxMatches, exceeded.MaxMatches)
			require.Equal(t, tt.wantMatches, exceeded.Matches)
			require.Equal(t, tt.wantAtLeast, exceeded.AtLeast)
			require.Equal(t, tt.dryRun, exceeded.DryRun, "a dry run applies the same gate")
			require.ErrorContains(t, err, "maxMatches")
		})
	}
}

func shardUUIDs(resolved []int) map[string][]strfmt.UUID {
	shardDocIDs := make(map[string][]strfmt.UUID, len(resolved))
	for i, count := range resolved {
		uuids := make([]strfmt.UUID, count)
		for j := range uuids {
			uuids[j] = strfmt.UUID(fmt.Sprintf("uuid-%d-%d", i, j))
		}
		shardDocIDs[fmt.Sprintf("shard-%d", i)] = uuids
	}
	return shardDocIDs
}
