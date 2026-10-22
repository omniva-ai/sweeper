/*
Copyright 2021 The Flux authors
Modifications copyright 2026 Omniva.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package jitter

import (
	"crypto/rand"
	"encoding/binary"
	mathrand "math/rand/v2"
	"time"
)

// NOTE: this is taken with edits from The Flux source code.
// https://github.com/fluxcd/pkg/blob/main/runtime/jitter/jitter.go

// Duration is a function that takes a duration and returns a modified duration
// with jitter added.
type Duration func(time.Duration) time.Duration

// NoJitter is a Duration function that will return the given duration without
// modification.
func NoJitter(d time.Duration) time.Duration {
	return d
}

// Percent returns a Duration function that will modify the given duration
// by a random percentage between 0 and p, with the sign chosen randomly.
//
// For example, if percent is 0.1, the returned Duration will modify the duration
// by a random percentage between -10% and 10%.
//
// When p <= 0 or p >= 1, duration is returned without a modification.
// If r is nil, a new rand.Rand will be created using the current time as the
// seed.
func Percent(p float64, r *mathrand.Rand) Duration {
	r = defaultOrRand(r)
	if p <= 0 || p >= 1 {
		return NoJitter
	}
	return func(d time.Duration) time.Duration {
		randomP := p * (2*r.Float64() - 1)
		return time.Duration(float64(d) * (1 + randomP))
	}
}

// defaultOrRand returns the given rand.Rand if it is not nil, otherwise it
// returns a new rand.Rand
func defaultOrRand(r *mathrand.Rand) *mathrand.Rand {
	if r == nil {
		var seed [16]byte
		_, _ = rand.Read(seed[:])
		s1 := binary.LittleEndian.Uint64(seed[:8])
		s2 := binary.LittleEndian.Uint64(seed[8:])
		return mathrand.New(mathrand.NewPCG(s1, s2)) //nolint:gosec // jitter does not require cryptographic randomness
	}
	return r
}
