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
	// CI feedback loop: after the PR is created, watch GitHub CI. On failure,
	// collect annotations and repair in the reused sandbox, force-pushing the
	// fix back to the same PR, until CI is green or the retry budget runs out.
	// GitHub-only: GitLab MR URLs have no /pull/N shape for watchAndRepairCI.
	if resultURL != "" && reportCIWatchEnabled() && task.Spec.Source.Provider == taskpkg.ProviderGitHub {
		fmt.Fprintf(out, "--- CI gathering check results for %s\n", resultURL)
		outcome, summary := watchAndRepairCI(out, task, resultURL, NewGitHubClient(), ciRepairRunnerFor(task, namespace, sandboxName, resultURL), resolveCIWatchOptions())
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

// ciClient abstracts the GitHub CI API for testability.
type ciClient interface {
	PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error)
	ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error)
	ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)
}

// ciRepairRunner repairs a CI failure inside the reused sandbox and drives the
// fix back to the PR branch.
type ciRepairRunner func(annotations []CheckRunAnnotation) error

// ciWatchOptions bounds the CI watch loop.
type ciWatchOptions struct {
	maxRetries     int
	maxWait        time.Duration
	pollInterval   time.Duration
	settleInterval time.Duration
}

// ciWatchOutcome is the result of the CI watch loop.
type ciWatchOutcome int

const (
	ciWatchGreen  ciWatchOutcome = 0
	ciWatchFailed ciWatchOutcome = 1
)

// watchAndRepairCI polls a PR's CI until green, red, or the wait budget
// expires. On red it invokes repair (which fixes and force-pushes, updating the
// PR head SHA) and re-polls, up to maxRetries. It returns the outcome and a
// failure summary for reporting.
func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string) {
	owner, repo, number, err := parsePullRequestURL(prURL)
	if err != nil {
		return ciWatchFailed, fmt.Sprintf("parse PR URL %q: %v", prURL, err)
	}
	var lastSummary string
	for attempt := 0; attempt < opts.maxRetries; attempt++ {
		sha, err := gh.PullRequestHeadSHA(context.Background(), owner, repo, number)
		if err != nil {
			return ciWatchFailed, fmt.Sprintf("get PR head sha: %v", err)
		}
		status, summary := pollCheckRuns(gh, owner, repo, sha, opts)
		lastSummary = summary
		switch status {
		case ciCheckGreen:
			fmt.Fprintf(out, "--- CI GREEN (%s)\n", sha)
			return ciWatchGreen, summary
		case ciCheckRed:
			annotations := collectFailedAnnotations(gh, owner, repo, sha)
			fmt.Fprintf(out, "--- CI FAILED on %s (attempt %d/%d); repairing\n%s", sha, attempt+1, opts.maxRetries, formatCIFailures(annotations))
			if err := repair(annotations); err != nil {
				return ciWatchFailed, fmt.Sprintf("repair failed: %v", err)
			}
		case ciCheckError:
			return ciWatchFailed, fmt.Sprintf("check-runs API error: %s", summary)
		default: // ciCheckPending with budget exhausted
			return ciWatchFailed, fmt.Sprintf("CI still pending after %s", opts.maxWait)
		}
	}
	return ciWatchFailed, fmt.Sprintf("CI still failing after %d repair attempts:\n%s", opts.maxRetries, lastSummary)
}

// pollCheckRuns polls check-runs until green/red or the wait budget expires.
// A green verdict requires a settle window: GitHub registers check runs
// lazily (especially reusable-workflow callers like "e2e_test / call_e2e"),
// so a first all-green sighting may still be missing jobs that are about to
// appear and fail. We only declare green when two consecutive polls after the
// settle interval observe the same set of check runs, all completed and
// non-failing.
func pollCheckRuns(gh ciClient, owner, repo, sha string, opts ciWatchOptions) (ciCheckStatus, string) {
	deadline := time.Now().Add(opts.maxWait)
	var confirmedNames map[string]bool
	for {
		runs, err := gh.ListCheckRuns(context.Background(), owner, repo, sha)
		if err != nil {
			return ciCheckError, fmt.Sprintf("list check runs: %v", err)
		}
		switch evaluateCheckRuns(runs) {
		case ciCheckGreen:
			names := checkRunNames(runs)
			if confirmedNames != nil && equalCheckRunSet(confirmedNames, names) {
				// Same non-nil set seen green across the settle window: stable, declare green.
				return ciCheckGreen, summarizeCheckRuns(runs)
			}
			// First (or changed) all-green observation: record it and wait the
			// settle window for late-registering check runs before confirming.
			confirmedNames = names
			if opts.settleInterval <= 0 || time.Now().Add(opts.settleInterval).After(deadline) {
				return ciCheckGreen, summarizeCheckRuns(runs)
			}
			time.Sleep(opts.settleInterval)
			continue
		case ciCheckRed:
			return ciCheckRed, summarizeCheckRuns(runs)
		case ciCheckError:
			return ciCheckError, "unexpected check-run API error"
		default: // pending
			confirmedNames = nil
		}
		if time.Now().After(deadline) {
			return ciCheckPending, fmt.Sprintf("CI pending after %s", opts.maxWait)
		}
		time.Sleep(opts.pollInterval)
	}
}

// checkRunNames returns the set of check-run names currently registered.
func checkRunNames(runs []CheckRun) map[string]bool {
	names := make(map[string]bool, len(runs))
	for _, r := range runs {
		names[r.Name] = true
	}
	return names
}

// equalCheckRunSet reports whether two sets of check-run names are identical
// (both nil counts as equal — used to confirm a stable green across polls).
func equalCheckRunSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if !b[name] {
			return false
		}
	}
	return true
}

// collectFailedAnnotations returns all failure annotations across failing check runs.
func collectFailedAnnotations(gh ciClient, owner, repo, sha string) []CheckRunAnnotation {
	runs, err := gh.ListCheckRuns(context.Background(), owner, repo, sha)
	if err != nil {
		return nil
	}
	var all []CheckRunAnnotation
	for _, r := range runs {
		if r.Status != "completed" || isNonFailingConclusion(r.Conclusion) {
			continue
		}
		anns, err := gh.ListCheckRunAnnotations(context.Background(), owner, repo, r.ID)
		if err != nil {
			continue
		}
		all = append(all, anns...)
	}
	return all
}

// ciRepairRunnerFor returns a repair runner that runs BuildCIRepairScript in the
// reused sandbox via kubectl exec.
func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string) ciRepairRunner {
	containerName := task.Spec.Sandbox.ContainerName
	if containerName == "" {
		containerName = "dev"
	}
	return func(annotations []CheckRunAnnotation) error {
		instructions := buildCIRepairInstructions(task.Spec.Work.Instructions, prURL, annotations)
		script, err := taskpkg.BuildCIRepairScript(task, instructions)
		if err != nil {
			return err
		}
		return runKubectl(nil, "exec", "-n", namespace, sandboxName, "-c", containerName, "--", "/bin/sh", "-lc", script)
	}
}

// resolveCIWatchOptions resolves the CI watch settings. Seed from the
// flag-resolved opts (cobra applies flag defaults), then override from
// ReadConfig so scripts/update-config.sh can hot-update them.
func resolveCIWatchOptions() ciWatchOptions {
	o := ciWatchOptions{maxRetries: opts.CIWatchMaxRetries, maxWait: opts.CIWatchMaxWait, pollInterval: opts.CIWatchPollInterval, settleInterval: opts.CIWatchSettleInterval}
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
	if v := taskpkg.ReadConfig("CI_WATCH_RETRY_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			o.pollInterval = d
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_SETTLE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			o.settleInterval = d
		}
	}
	return o
}

// reportCIWatchEnabled reports whether CI watching is enabled (opts flag, with
// CI_WATCH_ENABLED env/config override).
func reportCIWatchEnabled() bool {
	if v := taskpkg.ReadConfig("CI_WATCH_ENABLED"); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return opts.CIWatchEnabled
}
