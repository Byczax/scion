// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hbird

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// farFuture is an expiry far enough ahead that AssignColor never treats a color
// as expired during a test.
var farFuture = time.Now().Add(24 * time.Hour)

// testResIDBits is the wire ResID width used across the tests; matches the
// production RESID_BITS and gives a color space large enough that growth tests
// never hit the bound unless they mean to.
const testResIDBits = 22

// TestIntervalColorMapNodesForInterval test if the correct nodes of the IntervalColorMap cover the range
func TestIntervalColorMapNodesForInterval(t *testing.T) {
	icm := NewIntervalColorMap(8, testResIDBits)

	cases := []struct {
		name        string
		low, high   int
		wantIndices []int
	}{
		{
			name:        "single",
			low:         2,
			high:        2,
			wantIndices: []int{9},
		},
		{
			name:        "first",
			low:         0,
			high:        0,
			wantIndices: []int{7},
		},
		{
			name:        "last",
			low:         7,
			high:        7,
			wantIndices: []int{14},
		},
		{
			name:        "paired",
			low:         4,
			high:        5,
			wantIndices: []int{5},
		},
		{
			name:        "two_unpaired",
			low:         3,
			high:        4,
			wantIndices: []int{10, 11},
		},
		{
			name:        "full",
			low:         0,
			high:        7,
			wantIndices: []int{0},
		},
		{
			name:        "almost_full",
			low:         1,
			high:        6,
			wantIndices: []int{8, 13, 4, 5},
		},
		{
			name:        "almost_full2",
			low:         1,
			high:        7,
			wantIndices: []int{8, 4, 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := icm.nodesForInterval(tc.low, tc.high)
			if err != nil {
				t.Fatalf("nodesForInterval(%d,%d) error: %v", tc.low, tc.high, err)
			}
			var gotIndices []int
			for i, _ := range nodes {
				for j, _ := range icm.nodes {
					if nodes[i] == &icm.nodes[j] {
						gotIndices = append(gotIndices, j)
						break
					}
				}
			}
			//fmt.Println(tc.name, ": gotIndices =", gotIndices)
			if len(gotIndices) != len(tc.wantIndices) {
				t.Fatalf("got len=%d, want len=%d", len(gotIndices), len(tc.wantIndices))
			}
			// We'll compare slices ignoring order if needed, but the original test expects the same order.
			for i := range tc.wantIndices {
				if gotIndices[i] != tc.wantIndices[i] {
					t.Fatalf("node index mismatch at %d: got %d, want %d",
						i, gotIndices[i], tc.wantIndices[i])
				}
			}
		})
	}

	// Test invalid intervals

	eCases := []struct {
		name      string
		low, high int
		wantError error
	}{
		{
			name:      "flipped",
			low:       7,
			high:      0,
			wantError: fmt.Errorf("invalid interval query on color tree: "),
		},
		{
			name:      "outOfBound",
			low:       0,
			high:      8,
			wantError: fmt.Errorf("invalid interval query on color tree: "),
		},
	}
	for _, tc := range eCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := icm.nodesForInterval(tc.low, tc.high)
			if err == nil {
				t.Fatalf("nodesForInterval(%d,%d) does not error: %v, expected: %v", tc.low, tc.high, err, tc.wantError)
			} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
				t.Fatalf("nodesForInterval(%d,%d) incorrect error: %v, expected: %v", tc.low, tc.high, err, tc.wantError)
			}
		})
	}
}

// TestColorAssignment tests the color assignment for various intervals
func TestColorAssignment(t *testing.T) {
	intervals := []struct {
		low, high int
	}{
		{0, 0},
		{2, 2},
		{4, 4},
		{6, 6},
		{0, 1},
		{5, 6},
		{1, 3},
		{3, 5},
	}
	var assignedColors []uint32
	colorTree := NewIntervalColorMap(7, testResIDBits)

	for _, itv := range intervals {
		c, err := colorTree.firstFreeColor(itv.low, itv.high)
		if err != nil {
			t.Fatalf("firstFreeColor(%d..%d) failed: %v", itv.low, itv.high, err)
		}
		if err := colorTree.markUsedColor(c, itv.low, itv.high); err != nil {
			t.Fatalf("markUsedColor(%d, %d..%d) failed: %v", c, itv.low, itv.high, err)
		}
		assignedColors = append(assignedColors, c)
	}

	want := []uint32{0, 0, 0, 0, 1, 1, 2, 3}
	if len(assignedColors) != len(want) {
		t.Fatalf("got len=%d, want len=%d", len(assignedColors), len(want))
	}
	for i := range assignedColors {
		if assignedColors[i] != want[i] {
			t.Errorf("color at %d: got %d, want %d", i, assignedColors[i], want[i])
		}
	}

	colorTree = NewIntervalColorMap(7, testResIDBits)
	// Pin "now" to unix slot 0 so absolute slots equal the relative test indices.
	colorTree.now = func() time.Time { return time.Unix(0, 0) }
	assignedColors = []uint32{}
	for _, itv := range intervals {
		c, err := colorTree.AssignColor(itv.low, itv.high, farFuture)
		if err != nil {
			t.Fatalf("AssignColor(%d..%d) failed: %v", itv.low, itv.high, err)
		}
		assignedColors = append(assignedColors, c)
	}
	for i := range assignedColors {
		if assignedColors[i] != want[i] {
			t.Errorf("color at %d: got %d, want %d", i, assignedColors[i], want[i])
		}
	}

	// Test invalid assignments
	eCases := []struct {
		name      string
		low, high int
		color     uint32
		wantError error
	}{
		{
			name:      "flipped",
			low:       6,
			high:      0,
			color:     0,
			wantError: fmt.Errorf("invalid interval when marking colors in color tree: "),
		},
		{
			name:      "subtreeOutOfBound",
			low:       0,
			high:      6,
			color:     14,
			wantError: fmt.Errorf("trying to mark color for invalid index in markSubTree"),
		},
		{
			name:      "ancestorOutOfBound",
			low:       0,
			high:      6,
			color:     14,
			wantError: fmt.Errorf("trying to mark color for invalid index in markAncestors"),
		},
		{
			name:      "variableSize",
			low:       0,
			high:      1,
			color:     65,
			wantError: nil,
		},
		{
			name:      "allUsed",
			low:       0,
			high:      6,
			color:     0,
			wantError: fmt.Errorf("all bits used, no free color found"),
		},
		{
			name:      "invalidAssignColorInterval",
			low:       5,
			high:      0,
			color:     0,
			wantError: fmt.Errorf("invalid reservation span"),
		},
		{
			name:      "invalidIdxIterator",
			low:       2,
			high:      2,
			color:     0,
			wantError: fmt.Errorf("false"),
		},
		{
			name:      "invalidNodeIterator",
			low:       2,
			high:      2,
			color:     0,
			wantError: fmt.Errorf("false"),
		},
	}

	for _, tc := range eCases {
		switch tc.name {
		case "flipped":
			t.Run(tc.name, func(t *testing.T) {
				err := colorTree.markUsedColor(tc.color, tc.low, tc.high)
				if err == nil {
					t.Fatalf("markUsedColor(%d, %d..%d) does not error: %v, expected: %v",
						tc.color, tc.low, tc.high, err, tc.wantError)
				} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
					t.Fatalf("markUsedColor(%d, %d..%d) incorrect error: %v, expected: %v",
						tc.color, tc.low, tc.high, err, tc.wantError)
				}
			})
		case "subtreeOutOfBound":
			t.Run(tc.name, func(t *testing.T) {
				err := colorTree.markSubTree(tc.high*2+2, tc.color)
				if err == nil {
					t.Fatalf("markSubTree(%d, %d) does not error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
					t.Fatalf("markSubTree(%d, %d) incorrect error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				}
			})
		case "ancestorOutOfBound":
			t.Run(tc.name, func(t *testing.T) {
				err := colorTree.markAncestors(tc.high*4+6, tc.color)
				if err == nil {
					t.Fatalf("markAncestors(%d, %d) does not error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
					t.Fatalf("markAncestors(%d, %d) incorrect error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				}
			})
		case "variableSize":
			for i := 0; i < 65; i++ {
				_ = colorTree.markUsedColor(uint32(i), tc.low, tc.low)
			}
			t.Run(tc.name, func(t *testing.T) {
				_, err := colorTree.firstFreeColor(tc.low, tc.high)
				if err != tc.wantError {
					t.Fatalf("firstFreeColor((%d..%d) failed: %v",
						tc.low, tc.high, err)
				}
			})
		case "allUsed":
			// Bound the color space to exactly two chunks (2*wordSize colors) so
			// that filling both chunks truly exhausts it and no fresh chunk can grow.
			smallTree := NewIntervalColorMap(7, 7) // maxColors = 1<<7 = 128 = 2*wordSize
			smallTree.nodes[7].markUsedColor(2*wordSize - 1)
			smallTree.nodes[7].colorBits[0] = ^uint64(0)
			smallTree.nodes[7].colorBits[1] = ^uint64(0)
			t.Run(tc.name, func(t *testing.T) {
				_, err := smallTree.firstFreeColor(tc.low, tc.low)
				if err == nil {
					t.Fatalf("firstFreeColor(%d, %d) does not error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
					t.Fatalf("firstFreeColor(%d, %d) incorrect error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				}
			})
		case "invalidAssignColorInterval":
			t.Run(tc.name, func(t *testing.T) {
				icm := NewIntervalColorMap(2, testResIDBits)
				icm.now = func() time.Time { return time.Unix(0, 0) }
				_, err := icm.AssignColor(tc.low, tc.high, farFuture)
				if err == nil {
					t.Fatalf("AssignColor(%d, %d) does not error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				} else if !strings.Contains(err.Error(), tc.wantError.Error()) {
					t.Fatalf("AssignColor(%d, %d) incorrect error: %v, expected: %v",
						tc.high+1, tc.color, err, tc.wantError)
				}
			})
		case "invalidIdxIterator":
			t.Run(tc.name, func(t *testing.T) {
				icm := NewIntervalColorMap(1, testResIDBits)
				iter := NewNodeIdxIter(icm, tc.low, tc.high)
				idx, ok := iter.Next()
				if ok {
					t.Fatalf("idx, ok := iter.Next(), got idx=%d, ok=%v, expected: %v",
						idx, ok, tc.wantError)
				}
				_, ok2 := iter.Next()
				if ok2 {
					t.Fatalf("idx, ok2 := iter.Next(), got idx=%d, ok2=%v, expected: %v",
						idx, ok, tc.wantError)
				}
			})
		case "invalidNodeIterator":
			t.Run(tc.name, func(t *testing.T) {
				icm := NewIntervalColorMap(1, testResIDBits)
				iter := NewNodeIter(icm, tc.low, tc.high)
				idx, ok := iter.Next()
				if ok {
					t.Fatalf("idx, ok := iter.Next(), got idx=%d, ok=%v, expected: %v",
						idx, ok, tc.wantError)
				}
				_, ok2 := iter.Next()
				if ok2 {
					t.Fatalf("idx, ok2 := iter.Next(), got idx=%d, ok2=%v, expected: %v",
						idx, ok, tc.wantError)
				}
			})
		}
	}
}

// TestChunkGrowthAndBound checks that colors grow past a single 64-bit chunk and
// that assignment fails only once the whole [0, 1<<resIDBits) space is exhausted.
func TestChunkGrowthAndBound(t *testing.T) {
	// resIDBits = 7 => maxColors = 128, exactly two chunks.
	const resIDBits = 7
	const maxColors = 1 << resIDBits
	icm := NewIntervalColorMap(4, resIDBits)
	icm.now = func() time.Time { return time.Unix(0, 0) }

	for i := 0; i < maxColors; i++ {
		c, err := icm.AssignColor(0, 0, farFuture)
		if err != nil {
			t.Fatalf("AssignColor #%d failed unexpectedly: %v", i, err)
		}
		if c != uint32(i) {
			t.Fatalf("AssignColor #%d: got color %d, want %d", i, c, i)
		}
	}
	// Color 64 must have been reachable (proves growth past one chunk).
	// The next assignment must fail: the space is full.
	if _, err := icm.AssignColor(0, 0, farFuture); err == nil {
		t.Fatalf("AssignColor past maxColors=%d did not error", maxColors)
	}
}

// TestTimeOverlapDistinct checks that reservations overlapping in time get
// distinct colors while time-disjoint ones reuse a color.
func TestTimeOverlapDistinct(t *testing.T) {
	icm := NewIntervalColorMap(100, testResIDBits)
	icm.now = func() time.Time { return time.Unix(0, 0) }

	// A: slots [0..10], B: slots [5..15] overlap A -> must differ.
	a, err := icm.AssignColor(0, 10, time.Unix(11, 0))
	if err != nil {
		t.Fatalf("assign A: %v", err)
	}
	b, err := icm.AssignColor(5, 15, time.Unix(16, 0))
	if err != nil {
		t.Fatalf("assign B: %v", err)
	}
	if a == b {
		t.Fatalf("overlapping reservations shared color %d", a)
	}
	// C: slots [20..30] disjoint from both -> may reuse the lowest color.
	c, err := icm.AssignColor(20, 30, time.Unix(31, 0))
	if err != nil {
		t.Fatalf("assign C: %v", err)
	}
	if c != 0 {
		t.Fatalf("disjoint reservation did not reuse color 0, got %d", c)
	}
}

// TestReuseAfterExpiry checks that once a reservation expires (wall clock passes
// its expiry), its color is released and reused.
func TestReuseAfterExpiry(t *testing.T) {
	icm := NewIntervalColorMap(100, testResIDBits)
	now := time.Unix(0, 0)
	icm.now = func() time.Time { return now }

	// Two overlapping reservations -> colors 0 and 1.
	c0, _ := icm.AssignColor(0, 10, time.Unix(10, 0))
	c1, _ := icm.AssignColor(0, 10, time.Unix(10, 0))
	if c0 == c1 {
		t.Fatalf("overlapping reservations shared a color")
	}
	// Advance past both expiries; the next overlapping assignment reuses color 0.
	now = time.Unix(20, 0)
	c2, err := icm.AssignColor(20, 25, time.Unix(26, 0))
	if err != nil {
		t.Fatalf("assign after expiry: %v", err)
	}
	if c2 != 0 {
		t.Fatalf("expected reused color 0 after expiry, got %d", c2)
	}
	if len(icm.reservations) != 1 {
		t.Fatalf("active reservations: got %d, want 1", len(icm.reservations))
	}
}

// TestBeyondHorizon checks that a reservation ending past the window is rejected.
func TestBeyondHorizon(t *testing.T) {
	icm := NewIntervalColorMap(64, testResIDBits) // 64-slot horizon
	icm.now = func() time.Time { return time.Unix(0, 0) }
	if _, err := icm.AssignColor(0, 64, farFuture); err == nil {
		t.Fatalf("reservation ending at horizon edge should be rejected")
	}
	if _, err := icm.AssignColor(0, 63, farFuture); err != nil {
		t.Fatalf("reservation within horizon rejected: %v", err)
	}
}
