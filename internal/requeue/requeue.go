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

package requeue

import (
	"time"

	"github.com/omniva-ai/sweeper/internal/jitter"
	ctrl "sigs.k8s.io/controller-runtime"
)

// None returns an empty controller result.
func None() (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// NoneWithError returns a empty controller result with the given error.
func NoneWithError(err error) (ctrl.Result, error) {
	return ctrl.Result{}, err
}

// WithDuration returns a controller result that will requeue after the given duration.
func WithDuration(dur time.Duration) (ctrl.Result, error) {
	return ctrl.Result{
		RequeueAfter: dur,
	}, nil
}

// WithDurationAndError returns a controller result that will requeue after the given duration and will return the given error.
func WithDurationAndError(dur time.Duration, err error) (ctrl.Result, error) {
	return ctrl.Result{
		RequeueAfter: dur,
	}, err
}

// WithJitter returns a controller result that will requeue after a jittered duration.
// You need to have called jitter.SetGlobalIntervalJitter first in your application.
func WithJitter(dur time.Duration) (ctrl.Result, error) {
	return ctrl.Result{
		RequeueAfter: jitter.JitteredIntervalDuration(dur),
	}, nil
}

// WithJitterAndError returns a controller result that will requeue after a jittered duration and will return the given error.
// You need to have called jitter.SetGlobalIntervalJitter first in your application.
func WithJitterAndError(dur time.Duration, err error) (ctrl.Result, error) {
	return ctrl.Result{
		RequeueAfter: jitter.JitteredIntervalDuration(dur),
	}, err
}
