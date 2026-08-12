package server

import (
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

func TestIsTriggerLabel(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"ai-factory-run", true},
		{"ai-factory-smoke", true},
		{"ai-factory-waiting", false},
		{"ai-factory-running", false},
		{"ai-factory", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTriggerLabel(tc.label); got != tc.want {
			t.Errorf("isTriggerLabel(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

func TestIsWaitingPhase(t *testing.T) {
	cases := []struct {
		phase string
		want  bool
	}{
		{taskpkg.PhasePending, true},
		{taskpkg.PhaseClaimCreated, true},
		{taskpkg.PhaseSandboxReady, false},
		{taskpkg.PhaseRunning, false},
		{taskpkg.PhaseSucceeded, false},
		{taskpkg.PhaseFailed, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isWaitingPhase(tc.phase); got != tc.want {
			t.Errorf("isWaitingPhase(%q) = %v, want %v", tc.phase, got, tc.want)
		}
	}
}

func TestCancelCommentBody(t *testing.T) {
	got := cancelCommentBody(42, "ai-factory-run")
	want := "ai-factory 已取消 #42：触发标签 ai-factory-run 被移除（任务尚未开始执行）"
	if got != want {
		t.Fatalf("cancelCommentBody() = %q, want %q", got, want)
	}
}