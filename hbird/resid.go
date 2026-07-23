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
	"errors"
	"sync"
	"time"
)

// wordSize is the number of ResID bits tracked per uint64 word in the bitset.
const wordSize = 64

// colorReservation records a single assigned color (ResID) together with the
// instant at which the reservation ends, so the color can be released and reused.
type colorReservation struct {
	color  uint32
	expiry time.Time
}

// IntervalColorMap hands out ResIDs ("colors") so that no two reservations that
// are active at the same time share one. A color is held from assignment until
// its reservation's expiry, after which it is released and may be reused.
//
// It is a lowest-free-bit allocator over a bitset that grows on demand up to
// maxColors (= 1<<resIDBits, the wire-format ResID space). Reservations that do
// not overlap in time transparently reuse colors because an expired reservation
// frees its bit. It is safe for concurrent use.
type IntervalColorMap struct {
	// mu guards all mutable state; AssignColor may be called concurrently from
	// multiple redemption handlers.
	mu sync.Mutex
	// used is a big-endian bitset: bit (wordSize-1-b) of used[i] set means color
	// i*wordSize+b is currently held.
	used []uint64
	// active holds every currently-held color with its expiry.
	active []colorReservation
	// maxColors is the exclusive upper bound on assignable colors (1<<resIDBits).
	maxColors uint32
}

// NewIntervalColorMap initializes an allocator over the color space
// [0, 1<<resIDBits). resIDBits is the number of bits available to encode a ResID
// on the wire.
func NewIntervalColorMap(resIDBits int) *IntervalColorMap {
	return &IntervalColorMap{
		maxColors: uint32(1) << resIDBits,
	}
}

// AssignColor releases any colors whose reservations have expired, then assigns
// and returns the lowest free color, held until expiry. It fails only once the
// entire [0, maxColors) space is occupied by still-active reservations.
func (icm *IntervalColorMap) AssignColor(expiry time.Time) (uint32, error) {
	icm.mu.Lock()
	defer icm.mu.Unlock()

	// Free expired colors first so their bits become reusable.
	icm.sweepExpired(time.Now())

	color, ok := icm.firstFree()
	if !ok {
		return 0, errors.New("all bits used, no free color found")
	}
	icm.mark(color, true)
	icm.active = append(icm.active, colorReservation{color: color, expiry: expiry})
	return color, nil
}

// sweepExpired releases every color whose reservation expired at or before now.
// Because each color is held by exactly one reservation, a plain per-color clear
// is correct here (no shared bits). Caller must hold icm.mu.
func (icm *IntervalColorMap) sweepExpired(now time.Time) {
	kept := icm.active[:0]
	for _, r := range icm.active {
		if !r.expiry.After(now) {
			icm.mark(r.color, false)
			continue
		}
		kept = append(kept, r)
	}
	icm.active = kept
}

// firstFree returns the lowest color not currently held, growing the color space
// on demand. It reports false only when [0, maxColors) is fully occupied.
// Caller must hold icm.mu.
func (icm *IntervalColorMap) firstFree() (uint32, bool) {
	allOnes := ^uint64(0)
	for i, w := range icm.used {
		if w == allOnes {
			continue
		}
		for b := uint32(0); b < wordSize; b++ {
			if w&(uint64(1)<<(wordSize-1-b)) == 0 {
				color := uint32(i)*wordSize + b
				if color >= icm.maxColors {
					return 0, false
				}
				return color, true
			}
		}
	}
	// Every allocated word is full: the next free color lives in a new word.
	color := uint32(len(icm.used)) * wordSize
	if color >= icm.maxColors {
		return 0, false
	}
	return color, true
}

// mark sets (used=true) or clears (used=false) the bit for color, growing the
// bitset as needed. Caller must hold icm.mu.
func (icm *IntervalColorMap) mark(color uint32, used bool) {
	idx := color / wordSize
	bit := uint64(1) << (wordSize - 1 - color%wordSize)
	if used {
		for uint32(len(icm.used)) <= idx {
			icm.used = append(icm.used, 0)
		}
		icm.used[idx] |= bit
		return
	}
	if idx < uint32(len(icm.used)) {
		icm.used[idx] &^= bit
	}
}
