/*
Copyright 2026 Omniva.

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
	mathrand "math/rand/v2"
	"testing"
	"time"
)

func TestPercentOutOfRangeReturnsDurationUnmodified(t *testing.T) {
	for _, p := range []float64{-0.5, 0, 1, 1.5} {
		if got := Percent(p, nil)(time.Minute); got != time.Minute {
			t.Errorf("Percent(%v)(1m) = %v, want 1m", p, got)
		}
	}
}

func TestPercentStaysWithinBounds(t *testing.T) {
	const p = 0.1
	d := 100 * time.Second
	jittered := Percent(p, mathrand.New(mathrand.NewPCG(1, 2)))
	lower := time.Duration(float64(d) * (1 - p))
	upper := time.Duration(float64(d) * (1 + p))

	for range 1000 {
		got := jittered(d)
		if got < lower || got > upper {
			t.Fatalf("jittered(%v) = %v, want within [%v, %v]", d, got, lower, upper)
		}
	}
}

func TestPercentIsDeterministicForSeededRand(t *testing.T) {
	a := Percent(0.2, mathrand.New(mathrand.NewPCG(7, 8)))
	b := Percent(0.2, mathrand.New(mathrand.NewPCG(7, 8)))

	for range 10 {
		if x, y := a(time.Minute), b(time.Minute); x != y {
			t.Fatalf("identically seeded jitter diverged: %v != %v", x, y)
		}
	}
}
