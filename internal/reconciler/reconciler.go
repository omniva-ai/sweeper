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

package reconciler

import (
	"context"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// Reconciler represents the methods for reconciling a resource.
type Reconciler interface {
	// Name returns the name of the reconciler.
	Name() string
	// Reconcile provides the implementation to reconcile a resource.
	Reconcile(context.Context) (ctrl.Result, error)
	// Delete provides the implementation to delete a resource.
	Delete(context.Context) (ctrl.Result, error)
}

// Watcher represents a reconciler that needs to enqueue a reconciliation
// in the parent controller for a watch object.
type Watcher interface {
	Watches() []WatchFunc
}

// WatchFunc is a function that returns a watch
type WatchFunc func(ctrl.Manager, logr.Logger, client.Object) Watch

// Watch represents a watch.
type Watch struct {
	Object       client.Object
	EventHandler handler.EventHandler
	Options      []builder.WatchesOption
}
