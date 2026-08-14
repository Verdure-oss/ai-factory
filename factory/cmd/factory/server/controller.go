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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
)

// factoryTaskList is a Kubernetes list of FactoryTasks.
type factoryTaskList struct {
	Items []taskpkg.FactoryTask `json:"items"`
}

// taskTracker tracks which tasks are currently being processed.
type taskTracker struct {
	mu      sync.Mutex
	running map[string]bool
}

func newTaskTracker() *taskTracker {
	return &taskTracker{
		running: make(map[string]bool),
	}
}

func (t *taskTracker) tryAcquire(namespace, name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := namespace + "/" + name
	if t.running[key] {
		return false
	}
	t.running[key] = true
	return true
}

func (t *taskTracker) release(namespace, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := namespace + "/" + name
	delete(t.running, key)
}

func startControllerLoop(ctx context.Context, cmd *cobra.Command) error {
	tracker := newTaskTracker()
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "default"
	}

	// Concurrency gate: at most maxConcurrent tasks execute at once.
	// Tasks beyond the limit are marked queued (ai-factory-waiting) in their own
	// poll cycle and retried until a slot frees up. maxConcurrent <= 0 disables
	// the gate (unlimited).
	maxConcurrent := resolveMaxConcurrentTasks(cmd)
	var sem chan struct{}
	if maxConcurrent > 0 {
		sem = make(chan struct{}, maxConcurrent)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "controller starting, watching namespace %s\n", namespace)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		tasks, err := listFactoryTasks(namespace)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "list tasks error: %v\n", err)
			time.Sleep(opts.WatchInterval)
			continue
		}

		for i := range tasks {
			task := tasks[i]
			if !shouldReconcile(task) {
				continue
			}
			if !tracker.tryAcquire(namespaceForTask(&task), task.Metadata.Name) {
				continue
			}
			if sem != nil {
				select {
				case sem <- struct{}{}:
					// slot acquired
				default:
					// concurrency full: mark queued, retry next poll cycle
					tracker.release(namespaceForTask(&task), task.Metadata.Name)
					markTaskQueued(cmd.ErrOrStderr(), &task)
					continue
				}
			}
			go func(t taskpkg.FactoryTask) {
				defer func() {
					if sem != nil {
						<-sem
					}
					tracker.release(namespaceForTask(&t), t.Metadata.Name)
				}()
				if err := executeTask(cmd.ErrOrStderr(), &t, opts.TaskTimeout); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "task %s/%s failed: %v\n", namespaceForTask(&t), t.Metadata.Name, err)
				}
			}(task)
		}

		time.Sleep(opts.WatchInterval)
	}
}

// resolveMaxConcurrentTasks resolves the concurrency limit with the following
// precedence:
//  1. --max-concurrent-tasks flag (when explicitly set),
//  2. MAX_CONCURRENT_TASKS via ReadConfig (Secret/ConfigMap file, then env —
//     updateable through scripts/update-config.sh),
//  3. default 2.
//
// A resolved value <= 0 means unlimited (no gate).
func resolveMaxConcurrentTasks(cmd *cobra.Command) int {
	if cmd.Flags().Changed("max-concurrent-tasks") {
		return opts.MaxConcurrentTasks
	}
	if v := taskpkg.ReadConfig("MAX_CONCURRENT_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return maxConcurrentTasksDefault
}

// maxConcurrentTasksDefault is used when neither the flag nor the env var is set.
const maxConcurrentTasksDefault = 2

// markTaskQueued idempotently marks a FactoryTask as queued (Pending + Queued
// reason) when the concurrency gate is full, and sets the ai-factory-waiting
// GitHub label so users see it is waiting for a free sandbox slot.
func markTaskQueued(out io.Writer, task *taskpkg.FactoryTask) {
	if isTaskQueued(task) {
		return
	}
	if err := patchTaskStatus(namespaceForTask(task), task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:   taskpkg.PhasePending,
		Reason:  "Queued",
		Message: "Waiting for a free sandbox slot (max concurrent tasks reached)",
	}); err != nil {
		fmt.Fprintf(out, "mark queued %s: %v\n", task.Metadata.Name, err)
		return
	}
	setIssueWaitingLabel(task)
}

// isTaskQueued reports whether a task is already marked as queued, so the
// controller does not patching/label-spam it every poll cycle.
func isTaskQueued(task *taskpkg.FactoryTask) bool {
	if task.Status.Phase != taskpkg.PhasePending {
		return false
	}
	for _, c := range task.Status.Conditions {
		if c.Type == taskpkg.PhasePending && c.Reason == "Queued" {
			return true
		}
	}
	return false
}

// setIssueWaitingLabel sets the GitHub ai-factory-waiting label for a task.
func setIssueWaitingLabel(task *taskpkg.FactoryTask) {
	if task.Spec.Source.Provider != taskpkg.ProviderGitHub {
		return
	}
	gh := NewGitHubClient()
	if !gh.HasToken() {
		return
	}
	repo := task.Spec.Source.Repository
	issueNum := 0
	fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
	if repo == "" || issueNum <= 0 {
		return
	}
	_ = gh.SetTaskWaiting(context.Background(), repo, issueNum)
}

func shouldReconcile(task taskpkg.FactoryTask) bool {
	switch task.Status.Phase {
	case "", taskpkg.PhasePending, taskpkg.PhaseClaimCreated, taskpkg.PhaseSandboxReady, taskpkg.PhaseRunning:
		return true
	default:
		return false
	}
}

func executeTask(out io.Writer, task *taskpkg.FactoryTask, timeout time.Duration) error {
	output, err := taskpkg.Reconcile(task)
	if err != nil {
		return err
	}
	manifest, err := output.SandboxClaimYAML()
	if err != nil {
		return err
	}

	namespace := output.SandboxClaim.Metadata.Namespace
	claim := output.SandboxClaim.Metadata.Name
	// Clean up SandboxClaim when task reaches terminal state (succeeded or failed).
	// TTL on the claim is a safety net; this is the primary cleanup path.
	defer cleanupSandboxClaim(namespace, claim)

	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhasePending,
		Reason:           "TaskAccepted",
		Message:          "FactoryTask accepted by controller",
		SandboxClaimName: claim,
	}); err != nil {
		return err
	}
	// Post "started" comment once (not in webhook, to avoid duplicates from label events)
	postStartedComment(task)
	if err := runKubectlWithInput(manifest, "apply", "-f", "-"); err != nil {
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:   taskpkg.PhaseFailed,
			Reason:  "SandboxClaimApplyFailed",
			Message: err.Error(),
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("SandboxClaim apply failed: %v", err))
		return err
	}
	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhaseClaimCreated,
		Reason:           "SandboxClaimCreated",
		Message:          "SandboxClaim created",
		SandboxClaimName: claim,
	}); err != nil {
		return err
	}
	// Update GitHub label to "waiting" while queued for a warm pool pod
	updateIntermediateLabel(task, taskpkg.PhaseClaimCreated)
	// Use polling instead of kubectl wait (kubectl wait has issues with some CRDs)
	if err := waitForSandboxClaimReady(namespace, claim, timeout); err != nil {
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseFailed,
			Reason:           "SandboxClaimReadyTimeout",
			Message:          err.Error(),
			SandboxClaimName: claim,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("SandboxClaim ready wait failed: %v", err))
		return err
	}

	// Cancellation guard: the task may have been deleted (trigger label removed)
	// while we waited for the sandbox to become ready. Abort quietly instead of
	// running the plan — the cancellation path already cleaned up labels.
	if taskCancelled(out, namespace, task.Metadata.Name) {
		return nil
	}

	sandboxName, err := kubectlOutput("get", "sandboxclaim", claim, "-n", namespace, "-o", "jsonpath={.status.sandbox.name}")
	if err != nil {
		return err
	}
	sandboxName = strings.TrimSpace(sandboxName)
	if sandboxName == "" {
		return fmt.Errorf("sandboxclaim %s/%s is ready but status.sandbox.name is empty", namespace, claim)
	}
	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhaseSandboxReady,
		Reason:           "SandboxReady",
		Message:          "SandboxClaim is ready",
		SandboxClaimName: claim,
		SandboxName:      sandboxName,
	}); err != nil {
		return err
	}
	// Update GitHub label back to "running" now that sandbox pod is ready
	updateIntermediateLabel(task, taskpkg.PhaseSandboxReady)
	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhaseRunning,
		Reason:           "PlanStarted",
		Message:          "Executing generated plan",
		SandboxClaimName: claim,
		SandboxName:      sandboxName,
	}); err != nil {
		return err
	}

	for _, step := range output.Plan.Steps {
		fmt.Fprintf(out, "--- RUN: %s\n", step.Name)
		if err := runKubectl(nil, append([]string{"exec", "-n", namespace, sandboxName, "-c", output.Plan.ContainerName, "--"}, step.Command...)...); err != nil {
			failure := taskpkg.ClassifyFailure(err.Error())
			if taskpkg.ShouldRetryFailure(failure) {
				fmt.Fprintf(out, "--- RETRY: %s after %s\n", step.Name, failure.Reason)
				_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
					Phase:            taskpkg.PhaseRunning,
					Reason:           "StepRetrying",
					Message:          fmt.Sprintf("%s failed with %s; retrying once", step.Name, failure.Reason),
					SandboxClaimName: claim,
					SandboxName:      sandboxName,
				})
				if retryErr := runKubectl(nil, append([]string{"exec", "-n", namespace, sandboxName, "-c", output.Plan.ContainerName, "--"}, step.Command...)...); retryErr == nil {
					continue
				} else {
					err = fmt.Errorf("%w\nretry failed: %v", err, retryErr)
					failure = taskpkg.ClassifyFailure(err.Error())
				}
			}
			_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
				Phase:            taskpkg.PhaseFailed,
				Reason:           "StepFailed",
				Message:          fmt.Sprintf("%s: %v", step.Name, err),
				SandboxClaimName: claim,
				SandboxName:      sandboxName,
				FailureReason:    failure,
			})
			reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("%s failed: %v", step.Name, err))
			return fmt.Errorf("%s: %w", step.Name, err)
		}
	}
	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhaseRunning,
		Reason:           "PlanCompleted",
		Message:          "FactoryTask plan completed; finalizing change request",
		SandboxClaimName: claim,
		SandboxName:      sandboxName,
	}); err != nil {
		return err
	}
	resultMessage := "FactoryTask completed successfully"
	changedFiles := collectChangedFiles(namespace, sandboxName, output.Plan.ContainerName)
	resultURL, changeRequestAlreadyExists, err := createTaskChangeRequest(task)
	if err != nil {
		failure := taskpkg.ClassifyFailure(err.Error())
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseFailed,
			Reason:           "ChangeRequestCreateFailed",
			Message:          err.Error(),
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
			FailureReason:    failure,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("Change request creation failed: %v", err))
		return err
	}
	if err := validateChangeRequestResult(task, resultURL, changeRequestAlreadyExists); err != nil {
		failure := taskpkg.ClassifyFailure(err.Error())
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseFailed,
			Reason:           "NoChangeRequest",
			Message:          err.Error(),
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
			FailureReason:    failure,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, err.Error())
		return err
	}
	// CI feedback loop: after the PR is created, wait for GitHub check events
	// (webhook-driven, no polling) and repair CI failures in the reused sandbox
	// until the quiet-window evaluation declares CI green or the budget runs out.
	// GitHub-only: GitLab MR URLs have no /pull/N shape for watchAndRepairCI.
	if resultURL != "" && reportCIWatchEnabled() && task.Spec.Source.Provider == taskpkg.ProviderGitHub {
		fmt.Fprintf(out, "--- CI gathering check results for %s\n", resultURL)
		watchOpts := resolveCIWatchOptions()
		outcome, summary := watchAndRepairCI(out, task, resultURL, NewGitHubClient(), ciRepairRunnerFor(task, namespace, sandboxName, resultURL, watchOpts), watchOpts)
		if outcome != ciWatchGreen {
			failure := taskpkg.FailureClassification{
				Reason:     taskpkg.CIFeedbackFailed,
				Friendly:   "GitHub CI did not pass after repair attempts",
				RawMessage: summary,
			}
			_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
				Phase:            taskpkg.PhaseFailed,
				Reason:           "CIFeedbackFailed",
				Message:          "GitHub CI failed after repair attempts: " + summary,
				SandboxClaimName: claim,
				SandboxName:      sandboxName,
				FailureReason:    failure,
			})
			reportTaskResult(out, task, taskpkg.PhaseFailed, "GitHub CI failed after repair attempts: "+summary)
			return fmt.Errorf("ci feedback failed: %s", summary)
		}
	}
	if changeRequestAlreadyExists {
		resultMessage = changeRequestReportMessage(task, resultURL, true, changedFiles)
		if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseSucceeded,
			Reason:           "ChangeRequestAlreadyExists",
			Message:          "Change request already exists",
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
			LastResultURL:    resultURL,
		}); err != nil {
			return err
		}
	} else if resultURL != "" {
		resultMessage = changeRequestReportMessage(task, resultURL, false, changedFiles)
		if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseSucceeded,
			Reason:           "ChangeRequestCreated",
			Message:          "Change request created",
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
			LastResultURL:    resultURL,
		}); err != nil {
			return err
		}
	} else {
		if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseSucceeded,
			Reason:           "PlanSucceeded",
			Message:          "FactoryTask completed successfully",
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
		}); err != nil {
			return err
		}
	}
	reportTaskResult(out, task, taskpkg.PhaseSucceeded, resultMessage)
	fmt.Fprintln(out, "PASS")
	return nil
}

// taskCancelled reports whether the FactoryTask was deleted (trigger label
// removed) while waiting for the sandbox to become ready. When cancelled it
// logs the cancellation and returns true; the caller must abort without doing
// any further work on the task — the cancellation path already cleaned up
// labels and posted its own comment.
func taskCancelled(out io.Writer, namespace, name string) bool {
	if taskExists(namespace, name) {
		return false
	}
	fmt.Fprintf(out, "--- CANCELLED: task %s deleted while waiting for sandbox\n", name)
	return true
}

func createTaskChangeRequest(task *taskpkg.FactoryTask) (string, bool, error) {
	if !opts.EnableChangeRequest || !task.Spec.ChangeRequest.Enabled {
		return "", false, nil
	}
	if task.Spec.ChangeRequest.PushOnly {
		return "", false, nil
	}
	result, err := taskpkg.CreateChangeRequest(context.Background(), task, taskpkg.ChangeRequestOptions{})
	if err != nil {
		if taskpkg.IsChangeRequestMissingBranch(err) {
			return "", false, fmt.Errorf("no change request created: source branch missing: %w", err)
		}
		return "", false, err
	}
	if result.AlreadyExists {
		return result.URL, true, nil
	}
	return result.URL, false, nil
}

func validateChangeRequestResult(task *taskpkg.FactoryTask, resultURL string, alreadyExists bool) error {
	if !opts.EnableChangeRequest || !task.Spec.ChangeRequest.Enabled || task.Spec.ChangeRequest.PushOnly || alreadyExists || strings.TrimSpace(resultURL) != "" {
		return nil
	}
	return fmt.Errorf("no change request created: provider returned no change request URL")
}

func postStartedComment(task *taskpkg.FactoryTask) {
	if !opts.ReportEnabled || task.Spec.Reporting.Mode != "comment" || task.Spec.Reporting.TargetURL == "" {
		return
	}
	body := fmt.Sprintf("ai-factory started processing this issue.\n\n- FactoryTask: %s/%s", namespaceForTask(task), task.Metadata.Name)
	if err := taskpkg.PostIssueComment(context.Background(), taskpkg.CommentReportOptions{
		Provider:  reportingProvider(task),
		TargetURL: task.Spec.Reporting.TargetURL,
		Body:      body,
		APIBase:   reportingAPIBase(task),
	}); err != nil {
		return
	}
}

func reportTaskResult(out io.Writer, task *taskpkg.FactoryTask, phase, message string) {
	// If the FactoryTask was deleted (e.g., cancelled by removing the trigger
	// label), skip all reporting — the cancellation path already cleaned up labels
	// and posted its own comment. Prevents a waiting goroutine whose claim was
	// deleted mid-flight from misreporting failure.
	if !taskExists(namespaceForTask(task), task.Metadata.Name) {
		return
	}
	// Always update labels regardless of comment reporting
	updateTaskLabels(task, phase)

	// Post result comment if reporting is enabled
	if !opts.ReportEnabled || task.Spec.Reporting.Mode != "comment" || task.Spec.Reporting.TargetURL == "" {
		return
	}
	fc := taskpkg.ClassifyFailure(message)
	body := buildReportMessage(task, phase, fc)
	if err := taskpkg.PostIssueComment(context.Background(), taskpkg.CommentReportOptions{
		Provider:  reportingProvider(task),
		TargetURL: task.Spec.Reporting.TargetURL,
		Body:      body,
		APIBase:   reportingAPIBase(task),
	}); err != nil {
		fmt.Fprintf(out, "--- REPORT FAILED: %v\n", err)
		return
	}
	fmt.Fprintf(out, "--- REPORT: comment %s\n", task.Spec.Reporting.TargetURL)
}

func updateTaskLabels(task *taskpkg.FactoryTask, phase string) {
	if task.Spec.Source.Provider != taskpkg.ProviderGitHub {
		return
	}
	gh := NewGitHubClient()
	if !gh.HasToken() {
		return
	}
	repo := task.Spec.Source.Repository
	issueNum := 0
	fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
	if repo == "" || issueNum <= 0 {
		return
	}
	ctx := context.Background()
	switch phase {
	case taskpkg.PhaseSucceeded:
		_ = gh.SetTaskDone(ctx, repo, issueNum)
	case taskpkg.PhaseFailed:
		_ = gh.SetTaskFailed(ctx, repo, issueNum)
	}
}

// updateIntermediateLabel sets the appropriate GitHub label during task execution.
// Called when transitioning between ClaimCreated (waiting) and SandboxReady (running).
func updateIntermediateLabel(task *taskpkg.FactoryTask, phase string) {
	if task.Spec.Source.Provider != taskpkg.ProviderGitHub {
		return
	}
	gh := NewGitHubClient()
	if !gh.HasToken() {
		return
	}
	repo := task.Spec.Source.Repository
	issueNum := 0
	fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
	if repo == "" || issueNum <= 0 {
		return
	}
	ctx := context.Background()
	switch phase {
	case taskpkg.PhaseClaimCreated:
		_ = gh.SetTaskWaiting(ctx, repo, issueNum)
	case taskpkg.PhaseSandboxReady:
		_ = gh.SetTaskRunning(ctx, repo, issueNum)
	}
}

func buildReportMessage(task *taskpkg.FactoryTask, phase string, fc taskpkg.FailureClassification) string {
	name := task.Metadata.Name
	if ns := namespaceForTask(task); ns != "" {
		name = ns + "/" + name
	}
	message := fc.RawMessage
	if phase == taskpkg.PhaseFailed {
		message = taskpkg.FriendlyFailureMessage(fc)
	}
	return fmt.Sprintf("FactoryTask `%s` %s\n\n%s", name, phase, message)
}

func changeRequestReportMessage(task *taskpkg.FactoryTask, resultURL string, alreadyExists bool, changedFiles []string) string {
	status := "A change request was created for this FactoryTask."
	nextStepAction := "Review the change request and approve or merge it when ready."
	if alreadyExists {
		status = "An existing change request is already open for this FactoryTask."
		nextStepAction = "Review the existing change request and decide whether to merge or close it."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FactoryTask completed successfully\n\n%s", status)
	if resultURL != "" {
		fmt.Fprintf(&b, "\n\n**%s:** %s", changeRequestKind(task), resultURL)
	}

	b.WriteString("\n\n### Changed files")
	if len(changedFiles) == 0 {
		b.WriteString("\nSee the change request above for the full file list.")
	} else {
		for _, file := range changedFiles {
			fmt.Fprintf(&b, "\n- `%s`", file)
		}
	}

	b.WriteString("\n\n### Validation")
	if len(task.Spec.Work.Commands) == 0 {
		b.WriteString("\n- No validation command configured.")
	} else {
		for _, command := range task.Spec.Work.Commands {
			fmt.Fprintf(&b, "\n- `%s` passed before the change request was reported.", command)
		}
	}
	if task.Spec.ChangeRequest.Enabled {
		changeBranch, targetBranch := changeRequestBranches(task)
		fmt.Fprintf(&b, "\n\n### Branch\n- Source: `%s`\n- Target: `%s`", changeBranch, targetBranch)
	}
	fmt.Fprintf(&b, "\n\n### Next steps\n- %s\n- Verify the changed files and validation results above match your expectations.", nextStepAction)
	if alreadyExists {
		b.WriteString("\n- If the issue was re-triggered, the existing branch may have been updated with new commits.")
	}
	return b.String()
}

func changeRequestKind(task *taskpkg.FactoryTask) string {
	switch task.Spec.Source.Provider {
	case taskpkg.ProviderGitHub:
		return "GitHub pull request"
	case taskpkg.ProviderGitLab:
		return "GitLab merge request"
	default:
		return "Change request"
	}
}

func collectChangedFiles(namespace, sandboxName, containerName string) []string {
	script := "cd /workspace/repo && { git diff --name-only HEAD; git diff --name-only HEAD~1 HEAD 2>/dev/null || true; } | sort -u"
	output, err := kubectlOutput("exec", "-n", namespace, sandboxName, "-c", containerName, "--", "/bin/sh", "-lc", script)
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ".ai-factory/") {
			continue
		}
		files = append(files, line)
	}
	return files
}

func changeRequestBranches(task *taskpkg.FactoryTask) (string, string) {
	targetBranch := task.Spec.ChangeRequest.TargetBranch
	if targetBranch == "" {
		targetBranch = task.Spec.Source.BaseRef
	}
	branchName := task.Spec.ChangeRequest.BranchName
	if branchName == "" {
		prefix := task.Spec.ChangeRequest.BranchPrefix
		if prefix == "" {
			prefix = "factory-task"
		}
		branchName = fmt.Sprintf("%s/%s", strings.Trim(prefix, "/"), dnsLabelForReport(task.Metadata.Name))
	}
	return branchName, targetBranch
}

func dnsLabelForReport(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-.")
	if result == "" {
		return "factory-task"
	}
	if len(result) <= 63 {
		return result
	}
	return strings.Trim(result[:63], "-.")
}

func reportingProvider(task *taskpkg.FactoryTask) string {
	if task.Spec.Reporting.Provider != "" {
		return task.Spec.Reporting.Provider
	}
	return task.Spec.Source.Provider
}

// reportingAPIBase returns the API base override for the task's reporting
// provider, mirroring how NewGitHubClient honors GITHUB_API_BASE. It returns
// "" when the provider has no override configured, in which case
// PostIssueComment uses the provider default.
func reportingAPIBase(task *taskpkg.FactoryTask) string {
	if reportingProvider(task) == taskpkg.ProviderGitHub {
		return taskpkg.ReadConfig("GITHUB_API_BASE")
	}
	return ""
}

func patchTaskStatus(namespace, name string, opts taskpkg.StatusPatchOptions) error {
	patch, err := taskpkg.StatusMergePatch(opts)
	if err != nil {
		return err
	}
	return runKubectl(nil, "patch", "factorytask", name, "-n", namespace, "--type=merge", "--subresource=status", "-p", string(patch))
}

func listFactoryTasks(namespace string) ([]taskpkg.FactoryTask, error) {
	out, err := kubectlOutput("get", "factorytasks", "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list factoryTaskList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("decode FactoryTask list: %w", err)
	}
	return list.Items, nil
}

// taskExists is the seam the cancellation guards use to check whether a
// FactoryTask still exists. Tests override it to simulate cancellation
// without a live cluster.
var taskExists = factoryTaskExists

func factoryTaskExists(namespace, name string) bool {
	_, err := kubectlOutput("get", "factorytask", name, "-n", namespace)
	if err == nil {
		return true
	}
	if isNotFoundError(err) {
		return false
	}
	// Transient error (API-server blip, kubectl failure): conservatively
	// treat the task as still existing so the cancellation guards remain
	// no-ops and a healthy task is not silently dropped.
	return true
}

func namespaceForTask(task *taskpkg.FactoryTask) string {
	if task.Metadata.Namespace == "" {
		return "default"
	}
	return task.Metadata.Namespace
}

func runKubectl(stdin []byte, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return kubectlCommandError{
			args:   args,
			err:    err,
			stdout: stdout.String(),
			stderr: stderr.String(),
		}
	}
	return nil
}

func runKubectlWithInput(stdin []byte, args ...string) error {
	return runKubectl(stdin, args...)
}

func kubectlOutput(args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("kubectl %s: %s: %w", strings.Join(args, " "), stderr.String(), err)
	}
	return string(out), nil
}

type kubectlCommandError struct {
	args   []string
	err    error
	stdout string
	stderr string
}

func (e kubectlCommandError) Error() string {
	parts := []string{fmt.Sprintf("kubectl %s: %v", strings.Join(e.args, " "), e.err)}
	if out := summarizeCommandOutput("stdout", e.stdout); out != "" {
		parts = append(parts, out)
	}
	if out := summarizeCommandOutput("stderr", e.stderr); out != "" {
		parts = append(parts, out)
	}
	return strings.Join(parts, "\n")
}

func (e kubectlCommandError) Unwrap() error {
	return e.err
}

func summarizeCommandOutput(label, output string) string {
	output = strings.TrimSpace(redactSensitive(output))
	if output == "" {
		return ""
	}
	return fmt.Sprintf("%s tail:\n%s", label, tailString(output, 4000))
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return "... <truncated>\n" + value[len(value)-limit:]
}

func redactSensitive(value string) string {
	redacted := value
	for _, name := range []string{
		"OPENAI_API_KEY",
		"CODEX_API_KEY",
		"GITHUB_TOKEN",
		"GITLAB_TOKEN",
		"AI_FACTORY_GITHUB_TOKEN",
		"WEBHOOK_SECRET",
	} {
		if secret := taskpkg.ReadConfig(name); secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted:"+name+">")
		}
	}
	return redacted
}

// cleanupSandboxClaim deletes the SandboxClaim resource. Called when a task reaches terminal state.
func cleanupSandboxClaim(namespace, name string) {
	_ = runKubectl(nil, "delete", "sandboxclaim", name, "-n", namespace, "--ignore-not-found")
}

// waitForSandboxClaimReady polls the SandboxClaim until it's Ready or timeout is reached.
// This is a workaround for kubectl wait not working reliably with some CRDs.
func waitForSandboxClaimReady(namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		output, err := kubectlOutput("get", "sandboxclaim", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
		if err == nil && strings.TrimSpace(output) == "True" {
			return nil
		}
		if isNotFoundError(err) {
			// The claim was deleted out from under us (e.g. its task was
			// cancelled while waiting). Fail fast instead of polling until
			// the timeout so the caller's failure path runs promptly.
			return fmt.Errorf("SandboxClaim %s/%s no longer exists: %v", namespace, name, err)
		}
		// Not Ready yet, or a transient error (API-server blip): keep polling.
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("timeout waiting for SandboxClaim %s/%s to be Ready after %v", namespace, name, timeout)
}

// ciWatchOutcome is the terminal result of a CI watch/repair cycle.
type ciWatchOutcome int

const (
	// ciWatchGreen means CI became green (possibly after repairs).
	ciWatchGreen ciWatchOutcome = iota
	// ciWatchFailed means CI could not be made green within the budget.
	ciWatchFailed
)

// ciWatchOptions controls the event-driven CI watch loop and the repair
// script. There is intentionally no poll interval: the loop is woken by
// webhook events (via the ciWaiter registry) and only evaluates against the
// check-suites API after an event arrives. settleInterval is the short
// confirm window T (default 60s) that converges late-registering suites once
// every suite has completed; it never waits for CI to run.
type ciWatchOptions struct {
	maxRetries       int           // repair attempts before giving up the whole watch
	maxWait          time.Duration // total wall-clock budget; only a fallback when the event stream is lost
	settleInterval   time.Duration // confirm window T: quiet hold after all suites complete
	maxToolRounds    int           // OPENAI_MAX_TOOL_ROUNDS for the repair agent
	allowTestChanges bool          // default policy: test edits allowed when CI fails in tests
	logSnippetLines  int           // job-log snippet window centered on the error
}

// ciWatchDefaults* are defaults for tuning keys without a CLI flag: the repair
// agent's exploration budget and the job-log snippet window. The retry/wait/
// settle values come from opts.CIWatch* (flags, hot-reloadable via CI_WATCH_*).
const (
	ciWatchDefaultsMaxToolRounds   = 3
	ciWatchDefaultsLogSnippetLines = 20
)

// ciRepairRunner executes a single repair pass against the reused sandbox
// using the given failure evidence. It returns an error only when the repair
// script could not be built or the sandbox failed to run it; whether another
// pass is needed is decided by the next CI evaluation.
type ciRepairRunner func(annotations []CheckRunAnnotation, logSnippets []JobLogSnippet) error

// ciWaiter lets a webhook handler signal a blocked watch loop.
type ciWaiter struct {
	notify chan struct{}
}

var (
	ciRegistryMu sync.Mutex
	ciRegistry   = map[string]*ciWaiter{}
)

// registerWaiter registers the watch loop for key (owner/repo/branch) and
// returns the waiter the loop blocks on. The webhook handler wakes it via
// notifyWaiter with the same key.
func registerWaiter(key string) *ciWaiter {
	ciRegistryMu.Lock()
	defer ciRegistryMu.Unlock()
	w := &ciWaiter{notify: make(chan struct{}, 1)}
	ciRegistry[key] = w
	return w
}

// unregisterWaiter removes the waiter for key so a late webhook event for a
// finished watch is a no-op.
func unregisterWaiter(key string) {
	ciRegistryMu.Lock()
	defer ciRegistryMu.Unlock()
	delete(ciRegistry, key)
}

// notifyWaiter wakes the waiter for key without blocking. It is a no-op when
// no watch loop is registered for the key (already finished, or no loop yet).
func notifyWaiter(key string) {
	ciRegistryMu.Lock()
	w := ciRegistry[key]
	ciRegistryMu.Unlock()
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// notifyWaitersForRepo wakes every waiter registered under a repo prefix (e.g.
// "owner/repo/"). Used when a check_run event carries no head_branch: the
// branch key is unavailable, so we wake all watchers for the repo. A spurious
// wake only triggers one quiet-window evaluation against each waiter's own PR
// head, which stays correct — it can never misjudge, only waste one API call.
func notifyWaitersForRepo(repoPrefix string) {
	ciRegistryMu.Lock()
	wake := make([]*ciWaiter, 0, 4)
	for key, w := range ciRegistry {
		if strings.HasPrefix(key, repoPrefix) {
			wake = append(wake, w)
		}
	}
	ciRegistryMu.Unlock()
	for _, w := range wake {
		select {
		case w.notify <- struct{}{}:
		default:
		}
	}
}

// watchAndRepairCI waits for GitHub check events on the PR, evaluates the
// check suites after a confirm window, and repairs any failures by running the
// given repair runner in the reused sandbox. It keeps repairing — a repair
// pass that errors does not end the watch; the next attempt tries again —
// until CI is green or the budget (maxRetries / maxWait) is exhausted. It
// returns ciWatchGreen once CI is green, and ciWatchFailed otherwise. The PR
// gets two progress comments: one when the first failure is found (repair
// rounds pending announces) and one aggregated result on exit. Comments never
// break the watch — failures are logged only.
func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string) {
	owner, repo, number, err := parsePullRequestURL(prURL)
	if err != nil {
		return ciWatchFailed, fmt.Sprintf("parse PR URL %q: %v", prURL, err)
	}
	// branch name is deterministic and force-push-stable; matches webhook head_branch.
	changeBranch, _ := changeRequestBranches(task)
	key := fmt.Sprintf("%s/%s/%s", owner, repo, changeBranch)
	w := registerWaiter(key)
	defer unregisterWaiter(key)
	ctx := context.Background()
	deadline := time.Now().Add(opts.maxWait)
	var lastSummary string
	var roundsRepaired int
	announced := false

	comment := func(body string) {
		if err := gh.CommentOnIssue(ctx, owner, repo, number, body); err != nil {
			fmt.Fprintf(out, "--- PR progress comment skipped: %v\n", err)
		}
	}
	// Aggregated result comment fires exactly once on every exit path. outcome
	// is assigned before each return below, so the deferred closure sees it.
	outcome := ciWatchFailed
	defer func() {
		switch {
		case outcome == ciWatchGreen && roundsRepaired > 0:
			comment(fmt.Sprintf("🤖 CI green after %d repair round(s).", roundsRepaired))
		case outcome == ciWatchGreen:
			comment("🤖 CI green, no repairs needed.")
		case roundsRepaired > 0:
			comment(fmt.Sprintf("🤖 CI still failing after %d repair round(s):\n%s", roundsRepaired, lastSummary))
		default:
			comment(fmt.Sprintf("🤖 CI could not be made green: %s", lastSummary))
		}
	}()

	for attempt := 0; attempt < opts.maxRetries; attempt++ {
		fmt.Fprintf(out, "--- CI watch attempt %d/%d waiting for events on %s\n", attempt+1, opts.maxRetries, key)
		status, summary := waitForCIEvent(ctx, task, gh, owner, repo, number, w, opts, deadline)
		lastSummary = summary
		switch status {
		case ciCheckGreen:
			outcome = ciWatchGreen
			fmt.Fprintf(out, "--- CI GREEN\n")
			return outcome, summary
		case ciCheckRed:
			if !announced {
				comment(fmt.Sprintf("🤖 GitHub CI failed; starting automated repair (up to %d round(s)).", opts.maxRetries))
				announced = true
			}
			// The task may have been cancelled while we waited; do not spend a
			// repair round on a task that no longer exists.
			if !taskExists(namespaceForTask(task), task.Metadata.Name) {
				outcome = ciWatchFailed
				return outcome, "task cancelled while CI failing"
			}
			headSHA, shaErr := gh.PullRequestHeadSHA(ctx, owner, repo, number)
			if shaErr != nil {
				outcome = ciWatchFailed
				return outcome, fmt.Sprintf("get PR head sha: %v", shaErr)
			}
			annotations, annErr := collectFailedAnnotations(ctx, gh, owner, repo, headSHA)
			if annErr != nil {
				outcome = ciWatchFailed
				return outcome, fmt.Sprintf("collect annotations: %v", annErr)
			}
			runs, _ := gh.ListCheckRuns(ctx, owner, repo, headSHA)
			logSnippets, _ := collectFailedJobLogs(ctx, gh, owner, repo, runs, opts.logSnippetLines)
			fmt.Fprintf(out, "--- CI FAILED (attempt %d/%d); repairing\n%s", attempt+1, opts.maxRetries, formatCIFailures(annotations))
			if err := repair(annotations, logSnippets); err != nil {
				// A failed repair pass is not the end: log it and let the next
				// attempt try again, so the watch keeps repairing until CI is
				// green or the budget (maxRetries / maxWait) is exhausted.
				fmt.Fprintf(out, "--- REPAIR PASS %d/%d FAILED: %v\n", attempt+1, opts.maxRetries, err)
				continue
			}
			roundsRepaired++
			// Fall through to the next attempt: wait for the re-pushed commit's
			// check-suites to converge again before evaluating.
		case ciCheckError:
			outcome = ciWatchFailed
			return outcome, fmt.Sprintf("check-runs API error: %s", summary)
		default: // ciCheckPending with deadline hit
			outcome = ciWatchFailed
			return outcome, fmt.Sprintf("CI still pending after %s", opts.maxWait)
		}
	}
	outcome = ciWatchFailed
	return outcome, fmt.Sprintf("CI still failing after %d repair attempts:\n%s", opts.maxRetries, lastSummary)
}

// waitForCIEvent blocks until the check suites for the PR converge. It is
// driven by check_suite / check_run completed webhook events (via w.notify)
// rather than a settle timer, so a long CI flow is never misjudged as failed
// just because 90s passed with no event. Verdict rules:
//
//   - A completed suite with a non-passing conclusion is terminal: red, no need
//     to wait for the remaining suites or the confirm window.
//   - Green requires every suite to be completed AND the confirm window T
//     (opts.settleInterval) to elapse with no new suite registering. A new
//     suite membership restarts T; an ordinary recheck never does.
//   - An empty suite list is NOT converged (lazy registration windows, the
//     instant after a force-push) — it can never be judged green.
//   - maxWait only bounds total event-stream loss (webhook unconfigured/
//     unreachable); long-running CI is not affected by it.
//
// task is used only for the cancellation check (taskExists) at the observation
// point — the loop never polls for it.
func waitForCIEvent(ctx context.Context, task *taskpkg.FactoryTask, gh ciClient, owner, repo string, number int, w *ciWaiter, opts ciWatchOptions, deadline time.Time) (ciCheckStatus, string) {
	// known is the suite id set captured when the confirm window started; a
	// membership change means a late suite registered and we must re-converge.
	var known map[int64]bool
	var confirm *time.Timer
	var confirmC <-chan time.Time

	list := func() ([]CheckSuite, string, error) {
		// Re-fetch the PR head on every evaluation in case a repair
		// force-push advanced the branch between attempts.
		refreshed, err := gh.PullRequestHeadSHA(ctx, owner, repo, number)
		if err != nil {
			return nil, "", err
		}
		suites, err := gh.ListCheckSuites(ctx, owner, repo, refreshed)
		if err != nil {
			return nil, "", err
		}
		return suites, summarizeCheckSuites(suites), nil
	}
	startConfirm := func(suites []CheckSuite) {
		known = suiteIDs(suites)
		confirm = time.NewTimer(opts.settleInterval)
		confirmC = confirm.C
	}
	stopConfirm := func() {
		if confirm != nil {
			if !confirm.Stop() {
				select {
				case <-confirm.C:
				default:
				}
			}
			confirm = nil
		}
		confirmC = nil
		known = nil
	}

	// One bootstrap snapshot: the CI may already have finished before our
	// waiter registered (all events fired during setup). Without this the loop
	// would wait for a completed event that will never come. A single
	// snapshot is not polling.
	suites, summary, err := list()
	if err != nil {
		return ciCheckError, fmt.Sprintf("list check suites: %v", err)
	}
	if fastRedSuites(suites) {
		return ciCheckRed, summary
	}
	if allSuitesCompleted(suites) {
		startConfirm(suites)
	}

	for {
		select {
		case <-w.notify:
			// An event arrived: re-evaluate the current head's suites.
			suites, summary, err := list()
			if err != nil {
				return ciCheckError, fmt.Sprintf("list check suites: %v", err)
			}
			if fastRedSuites(suites) {
				stopConfirm()
				return ciCheckRed, summary
			}
			if !allSuitesCompleted(suites) {
				// Not converged yet: drop any window and keep waiting.
				stopConfirm()
				continue
			}
			if confirmC == nil {
				startConfirm(suites)
			} else if !sameSuiteIDs(known, suiteIDs(suites)) {
				// A new suite registered inside the window: restart it so that
				// suite's own late-registered siblings are still caught.
				stopConfirm()
				startConfirm(suites)
			}
			// Otherwise the window stays running untouched.
		case <-confirmC:
			// Window elapsed with no membership change: final convergence check.
			suites, summary, err := list()
			if err != nil {
				return ciCheckError, fmt.Sprintf("list check suites: %v", err)
			}
			if fastRedSuites(suites) {
				stopConfirm()
				return ciCheckRed, summary
			}
			if !allSuitesCompleted(suites) || !sameSuiteIDs(known, suiteIDs(suites)) {
				// The world moved exactly as the window closed: re-converge.
				if allSuitesCompleted(suites) {
					// A new suite registered: restart the window from it.
					stopConfirm()
					startConfirm(suites)
					continue
				}
				stopConfirm()
				continue
			}
			if !taskExists(namespaceForTask(task), task.Metadata.Name) {
				stopConfirm()
				return ciCheckPending, "task cancelled while waiting for CI"
			}
			stopConfirm()
			return evaluateCheckSuites(suites), summary
		case <-time.After(time.Until(deadline)):
			return ciCheckPending, fmt.Sprintf("CI events not observed before %s", opts.maxWait)
		}
	}
}

// resolveCIWatchOptions resolves CI watch/repair tuning. Start from the CLI
// flag defaults (cobra has already applied them to opts), then CI_WATCH_*
// env/secret-file values override via ReadConfig for hot-reload.
func resolveCIWatchOptions() ciWatchOptions {
	o := ciWatchOptions{
		maxRetries:       opts.CIWatchMaxRetries,
		maxWait:          opts.CIWatchMaxWait,
		settleInterval:   opts.CIWatchSettleInterval,
		allowTestChanges: true, // PR #929 classes of failures need test edits
		logSnippetLines:  ciWatchDefaultsLogSnippetLines,
		maxToolRounds:    ciWatchDefaultsMaxToolRounds, // inherited session makes full re-exploration wasteful
	}
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.maxRetries = n
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			o.maxWait = d
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_SETTLE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			o.settleInterval = d
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_TOOL_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.maxToolRounds = n
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_LOG_SNIPPET_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.logSnippetLines = n
		}
	}
	return o
}

// ciRepairRunnerFor builds the default ciRepairRunner: it renders the strict
// repair instructions from the collected annotations/log snippets, builds the
// repair script (which commits and force-pushes the fix), and executes it in
// the reused sandbox.
func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string, opts ciWatchOptions) ciRepairRunner {
	containerName := task.Spec.Sandbox.ContainerName
	if containerName == "" {
		containerName = "dev"
	}
	return func(annotations []CheckRunAnnotation, logSnippets []JobLogSnippet) error {
		instructions := buildCIRepairInstructions(task.Spec.Work.Instructions, prURL, annotations, logSnippets, opts.allowTestChanges, opts.logSnippetLines)
		script, err := taskpkg.BuildCIRepairScript(task, instructions, taskpkg.CIRepairOptions{
			SessionFile:   taskpkg.CISessionFile,
			MaxToolRounds: opts.maxToolRounds,
		})
		if err != nil {
			return err
		}
		return runKubectl(nil, "exec", "-n", namespace, sandboxName, "-c", containerName, "--", "/bin/sh", "-lc", script)
	}
}

// reportCIWatchEnabled reports whether CI watching is enabled: the
// CI_WATCH_ENABLED ConfigMap/secret value (hot-updateable via
// update-config.sh) wins, otherwise the --ci-watch flag default.
func reportCIWatchEnabled() bool {
	if v := taskpkg.ReadConfig("CI_WATCH_ENABLED"); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return opts.CIWatchEnabled
}
