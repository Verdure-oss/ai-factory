// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
)

func TestResolveMaxConcurrentTasks(t *testing.T) {
	// newCmd builds a fresh command so Changed() reflects only this subtest's flag sets.
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().IntVar(&opts.MaxConcurrentTasks, "max-concurrent-tasks", 0, "")
		return cmd
	}

	t.Run("flag wins over env", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "7")
		_ = cmd.Flags().Set("max-concurrent-tasks", "3")
		if got := resolveMaxConcurrentTasks(cmd); got != 3 {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want 3", got)
		}
	})

	t.Run("env used when flag unset", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "5")
		if got := resolveMaxConcurrentTasks(cmd); got != 5 {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want 5", got)
		}
	})

	t.Run("default when neither set", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "")
		if got := resolveMaxConcurrentTasks(cmd); got != maxConcurrentTasksDefault {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want %d", got, maxConcurrentTasksDefault)
		}
	})

	t.Run("invalid env falls back to default", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "abc")
		if got := resolveMaxConcurrentTasks(cmd); got != maxConcurrentTasksDefault {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want %d", got, maxConcurrentTasksDefault)
		}
	})
}

func TestIsTaskQueued(t *testing.T) {
	cases := []struct {
		name string
		status taskpkg.FactoryTaskStatus
		want  bool
	}{
		{
			name:   "empty status",
			status: taskpkg.FactoryTaskStatus{},
			want:   false,
		},
		{
			name:   "pending without queued reason",
			status: taskpkg.FactoryTaskStatus{Phase: taskpkg.PhasePending},
			want:   false,
		},
		{
			name: "pending with queued reason",
			status: taskpkg.FactoryTaskStatus{
				Phase: taskpkg.PhasePending,
				Conditions: []taskpkg.Condition{
					{Type: taskpkg.PhasePending, Reason: "Queued"},
				},
			},
			want: true,
		},
		{
			name: "running with stale queued condition",
			status: taskpkg.FactoryTaskStatus{
				Phase: taskpkg.PhaseRunning,
				Conditions: []taskpkg.Condition{
					{Type: taskpkg.PhasePending, Reason: "Queued"},
				},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &taskpkg.FactoryTask{Status: tc.status}
			if got := isTaskQueued(task); got != tc.want {
				t.Fatalf("isTaskQueued() = %v, want %v", got, tc.want)
			}
		})
	}
}