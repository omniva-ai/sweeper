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
	"math/rand/v2"
	"sync"
	"time"
)

// NOTE: this is taken with edits from The Flux source code.
// https://github.com/fluxcd/pkg/blob/main/runtime/jitter/interval.go

var (
	globalIntervalJitter     Duration = NoJitter
	globalIntervalJitterOnce sync.Once
)

// SetGlobalIntervalJitter sets the global interval jitter. It is safe to call
// this method multiple times, but only the first call will have an effect.
func SetGlobalIntervalJitter(p float64, r *rand.Rand) {
	globalIntervalJitterOnce.Do(func() {
		globalIntervalJitter = Percent(p, r)
	})
}

// JitteredIntervalDuration returns a jittered duration based on the given
// duration.
//
// To use this function, you must first initialize the global jitter with
// SetGlobalIntervalJitter.
func JitteredIntervalDuration(d time.Duration) time.Duration {
	return globalIntervalJitter(d)
}
