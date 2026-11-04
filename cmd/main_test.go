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

package main

import (
	"flag"
	"slices"
	"testing"
)

const kubeSystem = "kube-system"

func TestStringSliceFlag(t *testing.T) {
	tests := []struct {
		name    string
		initial []string
		sets    []string
		want    []string
	}{
		{name: "no Set keeps the default", initial: []string{kubeSystem}, want: []string{kubeSystem}},
		{name: "first Set replaces the default", initial: []string{kubeSystem}, sets: []string{"a"},
			want: []string{"a"}},
		{name: "repeated Set appends", sets: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "comma-separated values are split", sets: []string{"a,b,c"}, want: []string{"a", "b", "c"}},
		{name: "spaces are trimmed", sets: []string{" a , b "}, want: []string{"a", "b"}},
		{name: "empty parts are dropped", sets: []string{"a,,b"}, want: []string{"a", "b"}},
		{name: "only blank parts replace the default with nothing", initial: []string{kubeSystem},
			sets: []string{" , "}, want: nil},
		{name: "repeated and comma-separated values combine", initial: []string{kubeSystem},
			sets: []string{"a,b", "c"}, want: []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := slices.Clone(tt.initial)
			f := &stringSliceFlag{values: &values}
			for _, v := range tt.sets {
				if err := f.Set(v); err != nil {
					t.Fatalf("Set(%q) returned error: %v", v, err)
				}
			}
			if !slices.Equal(values, tt.want) {
				t.Errorf("values = %q, want %q", values, tt.want)
			}
		})
	}
}

func TestStringSliceFlagString(t *testing.T) {
	values := []string{"a", "b"}
	if got := (&stringSliceFlag{values: &values}).String(); got != "a,b" {
		t.Errorf("String() = %q, want %q", got, "a,b")
	}
	if got := (&stringSliceFlag{}).String(); got != "" {
		t.Errorf("String() on a zero-value flag = %q, want empty", got)
	}
}

func TestBindNamespaceFlags(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantProtected []string
		wantAllowed   []string
	}{
		{name: "defaults", wantProtected: []string{kubeSystem}},
		{name: "protected list replaces the default",
			args:          []string{"--protected-namespace=a,b", "--protected-namespace=c"},
			wantProtected: []string{"a", "b", "c"}},
		{name: "allowed list is repeatable and comma-separated",
			args:          []string{"--allow-protected-namespace=x", "--allow-protected-namespace=y,z"},
			wantProtected: []string{kubeSystem}, wantAllowed: []string{"x", "y", "z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			protected, allowed := bindNamespaceFlags(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.args, err)
			}
			if !slices.Equal(*protected, tt.wantProtected) {
				t.Errorf("protected = %q, want %q", *protected, tt.wantProtected)
			}
			if !slices.Equal(*allowed, tt.wantAllowed) {
				t.Errorf("allowed = %q, want %q", *allowed, tt.wantAllowed)
			}
			if !slices.Equal(defaultProtectedNamespaces, []string{kubeSystem}) {
				t.Errorf("defaultProtectedNamespaces = %q, want it unchanged", defaultProtectedNamespaces)
			}
		})
	}
}

func TestBindNamespaceFlagsRegistersFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	bindNamespaceFlags(fs)
	for _, name := range []string{"protected-namespace", "allow-protected-namespace"} {
		if fs.Lookup(name) == nil {
			t.Errorf("flag %q is not registered", name)
		}
	}
}
