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
	"fmt"
	"math"
	"sync"
	"time"
)

// Node stores the used colors in colorBits.
type Node struct {
	// colorBits stores indicator bits in big-endian form inside each uint64 chunk,
	// i.e. the highest-order bit of colorBits[i] corresponds to color = i*(wordSize) + 0
	colorBits []uint64
}

// wordSize, size in bits per entry in colorBits
const wordSize = 64

// markUsedColor sets the bit for the given color index in this node's colorBits.
func (n *Node) markUsedColor(color uint32) {
	chunkIdx := color / wordSize
	bitIdx := color % wordSize
	if uint32(len(n.colorBits)) <= chunkIdx {
		n.colorBits = append(n.colorBits, make([]uint64, chunkIdx-uint32(len(n.colorBits))+1)...)
	}
	n.colorBits[chunkIdx] |= 1 << (wordSize - 1 - bitIdx)
}

// colorReservation records a single assigned color together with the absolute
// time slots it occupies (unix-second granularity) and the instant at which it
// expires. Absolute slots are stored so the tree can be rebased onto a rolling
// window as wall-clock time advances. Expired reservations are released so their
// colors can be reused.
type colorReservation struct {
	color   uint32
	absLow  int // absolute start slot (unix second)
	absHigh int // absolute end slot (unix second), inclusive
	expiry  time.Time
}

// IntervalColorMap assigns ResIDs ("colors") so that reservations overlapping in
// time never share one, while time-disjoint reservations reuse colors.
//
// The tree covers a rolling window of NUnitIntervals one-second slots starting at
// base (an absolute unix second). Reservations are stored with absolute slots and
// re-projected onto the window whenever it advances; expired reservations are
// dropped, freeing their colors.
type IntervalColorMap struct {
	// mu guards all mutable state below; AssignColor may be called concurrently
	// from multiple redemption handlers.
	mu             sync.Mutex
	nodes          []Node
	NUnitIntervals int
	// base is the absolute slot (unix second) mapped to leaf 0 of the tree.
	base int
	// reservations holds every currently-active color assignment so that expired
	// ones can be swept and their colors freed for reuse.
	reservations []colorReservation
	// maxColors is the exclusive upper bound on assignable color values, i.e.
	// 1<<resIDBits. Colors grow into fresh chunks up to this bound.
	maxColors uint32
	// now returns the current time; overridable in tests.
	now func() time.Time
}

// NewIntervalColorMap initializes a new IntervalColorMap. nUnitIntervals is the
// window size in one-second slots (the horizon; a reservation must fit within it).
// resIDBits is the number of bits available to encode a color (ResID) on the wire;
// colors range over [0, 1<<resIDBits).
func NewIntervalColorMap(nUnitIntervals, resIDBits int) *IntervalColorMap {
	nInnerNodes := nextPowerOfTwo(nUnitIntervals) - 1
	nodes := make([]Node, nInnerNodes+nUnitIntervals)
	return &IntervalColorMap{
		nodes:          nodes,
		NUnitIntervals: nUnitIntervals,
		maxColors:      uint32(1) << resIDBits,
		now:            time.Now,
	}
}

// height returns the height of the tree
func (icm *IntervalColorMap) height() uint32 {
	return ilog2(nextPowerOfTwo(icm.NUnitIntervals)) + 1
}

// nodesForInterval returns the nodes covering [low..high].
func (icm *IntervalColorMap) nodesForInterval(low, high int) ([]*Node, error) {
	if low > high || high >= icm.NUnitIntervals {
		return nil, fmt.Errorf("invalid interval query on color tree: [%d..%d]", low, high)
	}
	iter := NewNodeIter(icm, low, high)
	var nodes []*Node
	for {
		idx, ok := iter.Next()
		if !ok {
			break
		}
		nodes = append(nodes, &icm.nodes[idx])
	}
	return nodes, nil
}

// markUsedColor marks the color as used in the [low, high] interval
func (icm *IntervalColorMap) markUsedColor(color uint32, low, high int) error {
	if low > high || high >= icm.NUnitIntervals {
		return fmt.Errorf("invalid interval when marking colors in color tree: [%d..%d]", low, high)
	}
	// For each node index discovered by NodeIdxIter, mark in the entire subtree and ancestors.
	iter := NewNodeIdxIter(icm, low, high)
	for {
		idx, ok := iter.Next()
		if !ok {
			break
		}
		if err := icm.markSubTree(int(idx), color); err != nil {
			return err
		}
		if err := icm.markAncestors(int(idx), color); err != nil {
			return err
		}
	}
	return nil
}

func (icm *IntervalColorMap) markSubTree(index int, color uint32) error {
	if index < 0 || index >= len(icm.nodes) {
		return errors.New("trying to mark color for invalid index in markSubTree")
	}
	icm.nodes[index].markUsedColor(color)
	left := index*2 + 1
	right := index*2 + 2
	if left < len(icm.nodes) {
		if err := icm.markSubTree(left, color); err != nil {
			return err
		}
	}
	if right < len(icm.nodes) {
		if err := icm.markSubTree(right, color); err != nil {
			return err
		}
	}
	return nil
}

func (icm *IntervalColorMap) markAncestors(index int, color uint32) error {
	for index > 0 {
		index = (index - 1) / 2
		if index < 0 || index >= len(icm.nodes) {
			return errors.New("trying to mark color for invalid index in markAncestors")
		}
		icm.nodes[index].markUsedColor(color)
	}
	return nil
}

// AssignColor assigns and returns a color that is free for the whole time span
// [absLow, absHigh], given as absolute one-second slots (e.g. unix seconds of the
// reservation's start and end). expiry is when the reservation ends; once passed
// the color is released and reused. The span must lie within the rolling window
// [now, now+NUnitIntervals). It is safe for concurrent use.
func (icm *IntervalColorMap) AssignColor(absLow, absHigh int, expiry time.Time) (uint32, error) {
	icm.mu.Lock()
	defer icm.mu.Unlock()

	// Slide the window onto the current time and drop expired reservations.
	icm.rebase(icm.now())

	low, high, err := icm.project(absLow, absHigh)
	if err != nil {
		return 0, err
	}
	color, err := icm.firstFreeColor(low, high)
	if err != nil {
		return 0, err
	}
	if err := icm.markUsedColor(color, low, high); err != nil {
		return 0, err
	}
	icm.reservations = append(icm.reservations, colorReservation{
		color:   color,
		absLow:  absLow,
		absHigh: absHigh,
		expiry:  expiry,
	})
	return color, nil
}

// project maps an absolute slot span onto the current window, returning relative
// tree slots. The past part of a still-active reservation (before the window) is
// clamped away, since only the future overlaps new reservations. A span that ends
// before the window or begins beyond the horizon is rejected.
func (icm *IntervalColorMap) project(absLow, absHigh int) (int, int, error) {
	if absLow > absHigh {
		return 0, 0, fmt.Errorf("invalid reservation span: [%d..%d]", absLow, absHigh)
	}
	low := absLow - icm.base
	high := absHigh - icm.base
	if low < 0 {
		low = 0
	}
	if high < 0 {
		return 0, 0, fmt.Errorf("reservation already in the past: end slot %d < window start %d",
			absHigh, icm.base)
	}
	if high >= icm.NUnitIntervals {
		return 0, 0, fmt.Errorf("reservation end beyond horizon: needs slot %d, window is %d wide",
			high, icm.NUnitIntervals)
	}
	return low, high, nil
}

// rebase slides the window so leaf 0 maps to the current slot, drops reservations
// that expired at or before now, and rebuilds the tree from the survivors. It is a
// no-op when the window has not advanced and nothing expired. Rebuilding (rather
// than clearing individual bits) is required because colors are shared across
// overlapping intervals via subtree/ancestor marking. Caller must hold icm.mu.
func (icm *IntervalColorMap) rebase(now time.Time) {
	nowSlot := int(now.Unix())

	kept := icm.reservations[:0]
	removed := false
	for _, r := range icm.reservations {
		if !r.expiry.After(now) {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	icm.reservations = kept

	// Nothing changed: the tree already reflects the active set for this window.
	if nowSlot == icm.base && !removed {
		return
	}
	icm.base = nowSlot

	// Clear all bits and re-mark the survivors at their new relative positions.
	for i := range icm.nodes {
		icm.nodes[i].colorBits = nil
	}
	for _, r := range icm.reservations {
		low, high, err := icm.project(r.absLow, r.absHigh)
		if err != nil {
			// Fully outside the new window (e.g. edge rounding); nothing to mark.
			continue
		}
		_ = icm.markUsedColor(r.color, low, high)
	}
}

// firstFreeColor is a helper function calling an iterator over the combined chunked data.
func (icm *IntervalColorMap) firstFreeColor(low, high int) (uint32, error) {
	// Obtain an iterator with node references covering [low..high].
	nodeIter, err := icm.nodesForInterval(low, high)
	if err != nil {
		return 0, err
	}
	// Each Node has a colorBits slice of uint. We want to combine them with | (bitwise OR).
	// Then find the first free bit using firstFreeFromChunkIter.
	chunks := []uint64{0}

	// Collect union of node colorBits by OR-ing across them.
	// The maximum length of colorBits among all nodes might differ, so we gather them.
	for _, nodeRef := range nodeIter {
		// nodeRef: pointer to Node
		// We gather its colorBits by OR-ing them into a final slice.
		nbits := len(nodeRef.colorBits)
		if len(chunks) < nbits {
			// Extend our main chunk slice
			oldLen := len(chunks)
			chunks = append(chunks, make([]uint64, nbits-oldLen)...)
		}
		for i := 0; i < nbits; i++ {
			// OR them in
			chunks[i] |= nodeRef.colorBits[i]
		}
	}
	// Now use firstFreeFromChunkIter to pick the first free color bit
	return firstFreeFromChunkIter(chunks, icm.maxColors)
}

// firstFreeFromChunkIter returns the first free color in chunks, i.e. the first
// zero bit. When every currently-allocated chunk is full it grows into a fresh
// chunk by returning len(chunks)*wordSize, provided that stays below maxColors.
// It fails only once the full [0, maxColors) color space is exhausted.
func firstFreeFromChunkIter(chunks []uint64, maxColors uint32) (uint32, error) {
	// We look for the first chunk which is not all 1 bits
	allOnes := ^uint64(0)
	for i, val := range chunks {
		if val != allOnes {
			// find first bit that is 0 in val
			for bitIdx := uint32(0); bitIdx < wordSize; bitIdx++ {
				mask := uint64(1) << (wordSize - 1 - bitIdx)
				if (val & mask) == 0 {
					// The color is i*wordSize + bitIdx
					color := uint32(i)*wordSize + bitIdx
					if color >= maxColors {
						return 0, errors.New("all bits used, no free color found")
					}
					return color, nil
				}
			}
		}
	}
	// Every allocated chunk is full: the next free color lives in a new chunk.
	next := uint32(len(chunks)) * wordSize
	if next >= maxColors {
		return 0, errors.New("all bits used, no free color found")
	}
	return next, nil
}

// NodeIdxIter iterates over the indices in the tree covering [low..high].
type NodeIdxIter struct {
	low, high   int
	level       uint32
	totalHeight uint32
	finished    bool
}

// NewNodeIdxIter constructs the iterator from an IntervalColorMap, low, high.
func NewNodeIdxIter(colorMap *IntervalColorMap, low, high int) *NodeIdxIter {
	return &NodeIdxIter{
		low:         low,
		high:        high,
		level:       colorMap.height() - 1,
		totalHeight: colorMap.height(),
		finished:    false,
	}
}

// Next returns (nodeIndex, ok).
func (ni *NodeIdxIter) Next() (idx uint32, ok bool) {
	if ni.finished || ni.low >= (1<<ni.totalHeight) {
		return idx, false
	}
	for {
		subintervalExp := ni.totalHeight - ni.level - 1
		subintervalSize := 1 << subintervalExp

		// Condition for the "low" alignment case
		if ni.low%subintervalSize == 0 &&
			ni.low%(2*subintervalSize) != 0 &&
			ni.high >= ni.low+subintervalSize-1 {
			idx = uint32((1<<ni.level - 1) + ni.low/subintervalSize)
			ni.low += subintervalSize
			return idx, true
		}

		// Condition for the "high" alignment case
		if (ni.high+1)%subintervalSize == 0 &&
			(ni.high+1)%(2*subintervalSize) != 0 &&
			ni.high >= ni.low+subintervalSize-1 {
			idx = uint32((1<<ni.level - 1) + ni.high/subintervalSize)
			if subintervalSize <= ni.high {
				ni.high -= subintervalSize
			} else {
				ni.high = 0
			}
			ok = true
		}

		if ni.level == 0 {
			ni.finished = true
			return idx, ok
		}
		ni.level--
		if ok {
			return idx, ok
		}
	}
}

// NodeIter returns the node indices for [low..high].
type NodeIter struct {
	idxIter *NodeIdxIter
	done    bool
}

// NewNodeIter constructs a NodeIter from IntervalColorMap, low, high.
func NewNodeIter(colorMap *IntervalColorMap, low, high int) *NodeIter {
	return &NodeIter{
		idxIter: NewNodeIdxIter(colorMap, low, high),
		done:    false,
	}
}

// Next returns (index, ok) of the next node covering [low..high].
func (niter *NodeIter) Next() (int, bool) {
	if niter.done {
		return 0, false
	}
	idx, ok := niter.idxIter.Next()
	if !ok {
		niter.done = true
		return 0, false
	}
	return int(idx), true
}

func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	pow := 1
	for pow < n {
		pow <<= 1
	}
	return pow
}

func ilog2(n int) uint32 {
	return uint32(math.Log2(float64(n)))
}
