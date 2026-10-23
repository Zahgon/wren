// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/optional/wren_opt_random.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import "time"

// well512 implements the well equidistributed long-period linear PRNG
// (WELL512a), the generator the random module is built on.
//
// https://en.wikipedia.org/wiki/Well_equidistributed_long-period_linear
type well512 struct {
	state [16]uint32
	index uint32
}

// advanceState steps the generator. Code from Chris Lomont's
// "Random Number Generators" (2008).
func (well *well512) advanceState() uint32 {
	var a, b, c, d uint32
	a = well.state[well.index]
	c = well.state[(well.index+13)&15]
	b = a ^ c ^ (a << 16) ^ (c << 15)
	c = well.state[(well.index+9)&15]
	c ^= c >> 11
	a = b ^ c
	well.state[well.index] = a
	d = a ^ ((a << 5) & 0xda442d24)

	well.index = (well.index + 15) & 15
	a = well.state[well.index]
	well.state[well.index] = a ^ b ^ d ^ (a << 2) ^ (b << 18) ^ (c << 28)
	return well.state[well.index]
}

// cRand reproduces glibc's rand(): the additive feedback generator that
// srand() and rand() use, whose exact output the seeded `Random` class
// depends on.
type cRand struct {
	table [31]int32
	front int
	rear  int
}

// newCRand reproduces glibc's srand(): the state is filled from a
// multiplicative congruential generator and the first 10 * 31 results of the
// additive feedback generator are discarded.
func newCRand(seed uint32) *cRand {
	r := &cRand{}
	if seed == 0 {
		seed = 1
	}
	word := int64(int32(seed))
	r.table[0] = int32(word)
	for i := 1; i < 31; i++ {
		// The congruential generator 16807 * x mod 2^31-1, computed the way
		// Schrage does so it stays inside 32 bits.
		hi := word / 127773
		lo := word % 127773
		word = 16807*lo - 2836*hi
		if word < 0 {
			word += 2147483647
		}
		r.table[i] = int32(word)
	}
	r.front = 3
	r.rear = 0
	for i := 0; i < 310; i++ {
		r.next()
	}
	return r
}

// next reproduces glibc's rand(): the additive feedback generator over a ring
// of 31 words, whose result is the sum with its lowest bit dropped.
func (r *cRand) next() int32 {
	r.table[r.front] = int32(uint32(r.table[r.front]) + uint32(r.table[r.rear]))
	result := int32(uint32(r.table[r.front]) >> 1)
	r.front = (r.front + 1) % 31
	r.rear = (r.rear + 1) % 31
	return result
}

func randomAllocate(vm *VM) {
	foreign := vm.SetSlotNewForeign(0, 0)
	foreign.Data = &well512{}
}

func wellOf(vm *VM) *well512 {
	return vm.GetSlotForeign(0).Data.(*well512)
}

// randomSeed0 seeds the generator from the current time, as the original does.
func randomSeed0(vm *VM) {
	well := wellOf(vm)
	r := newCRand(uint32(time.Now().Unix()))
	for i := 0; i < 16; i++ {
		well.state[i] = uint32(r.next())
	}
}

func randomSeed1(vm *VM) {
	well := wellOf(vm)
	r := newCRand(toUint32(vm.GetSlotDouble(1)))
	for i := 0; i < 16; i++ {
		well.state[i] = uint32(r.next())
	}
}

func randomSeed16(vm *VM) {
	well := wellOf(vm)
	for i := 0; i < 16; i++ {
		well.state[i] = toUint32(vm.GetSlotDouble(i + 1))
	}
}

func randomFloat(vm *VM) {
	well := wellOf(vm)

	// A double has 53 bits of mantissa, so 53 bits of random source data are
	// taken: 32 bits shifted left 21, then another 21 bits.
	result := float64(well.advanceState()) * (1 << 21)
	result += float64(well.advanceState() & ((1 << 21) - 1))

	// That is a number from 0 to 2^53, so divide by the range to get a double
	// from 0 to 1.0, half-inclusive.
	result /= 9007199254740992.0

	vm.SetSlotDouble(0, result)
}

func randomInt0(vm *VM) {
	well := wellOf(vm)
	vm.SetSlotDouble(0, float64(well.advanceState()))
}

func randomBindForeignClassAllocate(className string) ForeignMethodFn {
	if className != "Random" {
		return nil
	}
	return randomAllocate
}

func randomBindForeignMethod(className string, isStatic bool, signature string) ForeignMethodFn {
	if className != "Random" {
		return nil
	}
	switch signature {
	case "<allocate>":
		return randomAllocate
	case "seed_()":
		return randomSeed0
	case "seed_(_)":
		return randomSeed1
	case "seed_(_,_,_,_,_,_,_,_,_,_,_,_,_,_,_,_)":
		return randomSeed16
	case "float()":
		return randomFloat
	case "int()":
		return randomInt0
	}
	return nil
}
