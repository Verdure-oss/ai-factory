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
	mu       sync.Mutex
	running  map[string]bool
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
			go func(t taskpkg.FactoryTask) {
				defer tracker.release(namespaceForTask(&t), t.Metadata.Name)
				if err := executeTask(cmd.ErrOrStderr(), &t, opts.TaskTimeout); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "task %s/%s failed: %v\n", namespaceForTask(&t), t.Metadata.Name, err)
				}
			}(task)
		}

		time.Sleep(opts.WatchInterval)
	}
}

func shouldReconcile(task taskpkg.FactoryTask) bool {
	switch task.Status.Phase {
	case "", taskpkg.PhasePending:
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

	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhasePending,
		Reason:           "TaskAccepted",
		Message:          "FactoryTask accepted by controller",
		SandboxClaimName: claim,
	}); err != nil {
		return err
	}
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
	if err := runKubectl(nil, "wait", "sandboxclaim", claim, "-n", namespace, "--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseFailed,
			Reason:           "SandboxClaimReadyTimeout",
			Message:          err.Error(),
			SandboxClaimName: claim,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("SandboxClaim ready wait failed: %v", err))
		return err
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

func reportTaskResult(out io.Writer, task *taskpkg.FactoryTask, phase, message string) {
	if !opts.ReportEnabled || task.Spec.Reporting.Mode != "comment" || task.Spec.Reporting.TargetURL == "" {
		return
	}
	fc := taskpkg.ClassifyFailure(message)
	body := buildReportMessage(task, phase, fc)
	if err := taskpkg.PostIssueComment(context.Background(), taskpkg.CommentReportOptions{
		Provider:  reportingProvider(task),
		TargetURL: task.Spec.Reporting.TargetURL,
		Body:      body,
	}); err != nil {
		fmt.Fprintf(out, "--- REPORT FAILED: %v\n", err)
		return
	}
	fmt.Fprintf(out, "--- REPORT: comment %s\n", task.Spec.Reporting.TargetURL)

	// Update GitHub labels
	if task.Spec.Source.Provider == taskpkg.ProviderGitHub {
		gh := NewGitHubClient()
		if gh.HasToken() {
			repo := task.Spec.Source.Repository
			issueNum := 0
			fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
			if repo != "" && issueNum > 0 {
				ctx := context.Background()
				switch phase {
				case taskpkg.PhaseSucceeded:
					_ = gh.SetTaskDone(ctx, repo, issueNum)
				case taskpkg.PhaseFailed:
					_ = gh.SetTaskFailed(ctx, repo, issueNum)
				}
			}
		}
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
		if secret := os.Getenv(name); secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted:"+name+">")
		}
	}
	return redacted
}
