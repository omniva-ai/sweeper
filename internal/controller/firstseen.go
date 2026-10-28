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

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type firstSeenEntry struct {
	namespace, name string
	at              time.Time
}

// firstSeenTracker records, per pod UID, when the controller first saw the pod in the Failed
// phase. It is in memory only: after a restart or failover every pod gets a new first-seen time,
// which can only delay a deletion, never bring it forward. It is safe for concurrent use and is
// shared by all policies, so it is pruned by pod state, never by policy membership.
type firstSeenTracker struct {
	mu   sync.Mutex
	pods map[types.UID]firstSeenEntry
}

func newFirstSeenTracker() *firstSeenTracker {
	return &firstSeenTracker{pods: map[types.UID]firstSeenEntry{}}
}

// observe records now as the first sighting of pod unless one is already stored, and returns the
// stored time.
func (t *firstSeenTracker) observe(pod *corev1.Pod, now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.pods[pod.UID]; ok {
		return e.at
	}
	t.pods[pod.UID] = firstSeenEntry{namespace: pod.Namespace, name: pod.Name, at: now}
	return now
}

// prune drops the entries of pods that no longer exist, were replaced by a pod with a new UID,
// are no longer Failed, or are terminating. An entry whose pod cannot be read is kept, and the
// read errors are returned joined.
func (t *firstSeenTracker) prune(ctx context.Context, c client.Reader) error {
	t.mu.Lock()
	snapshot := make(map[types.UID]firstSeenEntry, len(t.pods))
	for uid, e := range t.pods {
		snapshot[uid] = e
	}
	t.mu.Unlock()

	var errs error
	var stale []types.UID
	for uid, e := range snapshot {
		var pod corev1.Pod
		err := c.Get(ctx, client.ObjectKey{Namespace: e.namespace, Name: e.name}, &pod)
		switch {
		case apierrors.IsNotFound(err):
			stale = append(stale, uid)
		case err != nil:
			errs = errors.Join(errs, fmt.Errorf("getting pod %s/%s: %w", e.namespace, e.name, err))
		case pod.UID != uid, pod.Status.Phase != corev1.PodFailed, pod.DeletionTimestamp != nil:
			stale = append(stale, uid)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, uid := range stale {
		delete(t.pods, uid)
	}
	return errs
}
