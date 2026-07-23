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
	"sync"
	"testing"
	"time"
)

// testResIDBits mirrors the production RESID_BITS wire width.
const testResIDBits = 22

var farFuture = time.Now().Add(24 * time.Hour)

// TestAssignSequential checks that colors are handed out lowest-first and stay
// unique while every reservation is still active.
func TestAssignSequential(t *testing.T) {
	icm := NewIntervalColorMap(testResIDBits)
	for i := uint32(0); i < 200; i++ {
		c, err := icm.AssignColor(farFuture)
		if err != nil {
			t.Fatalf("AssignColor #%d failed: %v", i, err)
		}
		if c != i {
			t.Fatalf("AssignColor #%d: got %d, want %d", i, c, i)
		}
	}
}

// TestReuseAfterExpiry checks that an expired reservation's color is released and
// reused by the next assignment.
func TestReuseAfterExpiry(t *testing.T) {
	icm := NewIntervalColorMap(testResIDBits)

	past := time.Now().Add(-time.Second)
	// Assign three colors that are already expired.
	for i := 0; i < 3; i++ {
		if _, err := icm.AssignColor(past); err != nil {
			t.Fatalf("assign expired #%d: %v", i, err)
		}
	}
	// The next assignment sweeps the expired ones and reuses color 0.
	c, err := icm.AssignColor(farFuture)
	if err != nil {
		t.Fatalf("assign after expiry: %v", err)
	}
	if c != 0 {
		t.Fatalf("expected reused color 0, got %d", c)
	}
	// Exactly one reservation should remain active.
	if got := len(icm.active); got != 1 {
		t.Fatalf("active reservations: got %d, want 1", got)
	}
}

// TestNoReuseWhileActive checks that colors are NOT reused while their
// reservations are still active (overlapping reservations get distinct colors).
func TestNoReuseWhileActive(t *testing.T) {
	icm := NewIntervalColorMap(testResIDBits)
	seen := map[uint32]bool{}
	for i := 0; i < 500; i++ {
		c, err := icm.AssignColor(farFuture)
		if err != nil {
			t.Fatalf("assign #%d: %v", i, err)
		}
		if seen[c] {
			t.Fatalf("color %d handed out twice while active", c)
		}
		seen[c] = true
	}
}

// TestGrowthAndExhaustion checks that colors grow past a single 64-bit word and
// that assignment fails only once the whole [0, 1<<resIDBits) space is full.
func TestGrowthAndExhaustion(t *testing.T) {
	const resIDBits = 7 // maxColors = 128 = two words
	const maxColors = 1 << resIDBits
	icm := NewIntervalColorMap(resIDBits)

	for i := 0; i < maxColors; i++ {
		c, err := icm.AssignColor(farFuture)
		if err != nil {
			t.Fatalf("assign #%d failed unexpectedly: %v", i, err)
		}
		if c != uint32(i) {
			t.Fatalf("assign #%d: got %d, want %d (growth past word 0 failed?)", i, c, i)
		}
	}
	if _, err := icm.AssignColor(farFuture); err == nil {
		t.Fatalf("assignment past maxColors=%d did not error", maxColors)
	}
}

// TestConcurrentAssign checks the allocator is safe under concurrent use and
// never hands out the same color twice.
func TestConcurrentAssign(t *testing.T) {
	icm := NewIntervalColorMap(testResIDBits)
	const goroutines, perG = 16, 100

	var mu sync.Mutex
	seen := map[uint32]bool{}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c, err := icm.AssignColor(farFuture)
				if err != nil {
					t.Errorf("assign: %v", err)
					return
				}
				mu.Lock()
				if seen[c] {
					t.Errorf("duplicate color %d", c)
				}
				seen[c] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != goroutines*perG {
		t.Fatalf("distinct colors: got %d, want %d", len(seen), goroutines*perG)
	}
}
