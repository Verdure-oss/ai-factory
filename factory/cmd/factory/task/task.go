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

// Package task contains FactoryTask CLI commands.
package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

// Cmd represents the task command.
var Cmd = &cobra.Command{
	Use:   "task",
	Short: "Manage factory tasks",
	Long:  `Subcommands for validating and inspecting FactoryTask resources.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var validateCmd = &cobra.Command{
	Use:   "validate [file]",
	Short: "Validate a FactoryTask YAML file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		task, err := taskpkg.Parse(data)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "--- PASS: validate %s\n", args[0])
		fmt.Fprintf(cmd.OutOrStdout(), "provider=%s repository=%s baseRef=%s agent=%s\n",
			task.Spec.Source.Provider,
			task.Spec.Source.Repository,
			task.Spec.Source.BaseRef,
			task.Spec.Agent.Name)
		return nil
	},
}

var planCmd = &cobra.Command{
	Use:   "plan [file]",
	Short: "Render the normalized execution plan for a FactoryTask YAML file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		task, err := taskpkg.Parse(data)
		if err != nil {
			return err
		}
		plan, err := taskpkg.BuildExecutionPlan(task)
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "task=%s provider=%s repository=%s\n", plan.TaskName, plan.Provider, plan.Repository)
		fmt.Fprintf(out, "cloneURL=%s baseRef=%s\n", plan.CloneURL, plan.BaseRef)
		if task.Spec.ChangeRequest.Enabled {
			fmt.Fprintf(out, "changeBranch=%s targetBranch=%s\n", plan.ChangeBranch, plan.TargetBranch)
		}
		fmt.Fprintf(out, "sandboxTemplate=%s sandboxClaim=%s container=%s agent=%s agentCommand=%s\n", plan.SandboxTemplate, plan.SandboxClaim, plan.ContainerName, plan.AgentName, plan.AgentCommand)
		for _, step := range plan.Steps {
			fmt.Fprintf(out, "- %s: %s\n", step.Name, strings.Join(step.Command, " "))
		}
		return nil
	},
}

var planDryRunCmd = &cobra.Command{
	Use:   "plan-dry-run [file]",
	Short: "Render the execution plan for a FactoryTask YAML file without creating resources",
	Long:  `Parses and validates a FactoryTask YAML, builds the execution plan, and prints the plan as YAML. No Kubernetes resources are created and no cluster is required.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		task, err := taskpkg.Parse(data)
		if err != nil {
			return fmt.Errorf("invalid FactoryTask YAML: %w", err)
		}
		plan, err := taskpkg.BuildExecutionPlan(task)
		if err != nil {
			return fmt.Errorf("build execution plan: %w", err)
		}
		out, err := yaml.Marshal(plan)
		if err != nil {
			return fmt.Errorf("marshal plan: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "Run FactoryTask controller operations",
	Long:  `Controller commands reconcile FactoryTask resources into sandbox work.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Report FactoryTask results to external systems",
	Long:  `Report commands write FactoryTask execution results back to provider-neutral targets.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var reportCommentOptions struct {
	message string
	token   string
	apiBase string
	dryRun  bool
}

var reportCommentCmd = &cobra.Command{
	Use:   "comment [file]",
	Short: "Write a FactoryTask result comment to GitHub or GitLab",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task, err := readTask(args[0])
		if err != nil {
			return err
		}
		message := reportCommentOptions.message
		if strings.TrimSpace(message) == "" {
			message = buildReportMessageFromString(task, "Manual", "FactoryTask report requested manually")
		}
		opts := taskpkg.CommentReportOptions{
			Provider:  reportingProvider(task),
			TargetURL: task.Spec.Reporting.TargetURL,
			Body:      message,
			Token:     reportCommentOptions.token,
			APIBase:   reportCommentOptions.apiBase,
		}
		if reportCommentOptions.dryRun {
			if opts.Token == "" {
				opts.Token = "dry-run-token"
			}
			req, err := taskpkg.BuildCommentRequest(opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "provider=%s method=%s url=%s\n", req.Provider, req.Method, req.URL)
			fmt.Fprintf(cmd.OutOrStdout(), "body=%s\n", string(req.Body))
			return nil
		}
		if err := taskpkg.PostIssueComment(context.Background(), opts); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "--- PASS: comment %s\n", task.Spec.Reporting.TargetURL)
		return nil
	},
}

var changeRequestCmd = &cobra.Command{
	Use:   "change-request",
	Short: "Manage FactoryTask pull requests and merge requests",
	Long:  `Change request commands create provider-native pull requests or merge requests from FactoryTask changeRequest specs.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var changeRequestCreateOptions struct {
	token   string
	apiBase string
	dryRun  bool
}

var changeRequestCreateCmd = &cobra.Command{
	Use:   "create [file]",
	Short: "Create a GitHub pull request or GitLab merge request for a FactoryTask",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task, err := readTask(args[0])
		if err != nil {
			return err
		}
		opts := taskpkg.ChangeRequestOptions{
			Token:   changeRequestCreateOptions.token,
			APIBase: changeRequestCreateOptions.apiBase,
		}
		if changeRequestCreateOptions.dryRun {
			if opts.Token == "" {
				opts.Token = "dry-run-token"
			}
			req, err := taskpkg.BuildChangeRequest(task, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "provider=%s method=%s url=%s\n", req.Provider, req.Method, req.URL)
			fmt.Fprintf(cmd.OutOrStdout(), "body=%s\n", string(req.Body))
			return nil
		}
		result, err := taskpkg.CreateChangeRequest(context.Background(), task, opts)
		if err != nil {
			return err
		}
		if result.AlreadyExists {
			fmt.Fprintln(cmd.OutOrStdout(), "--- PASS: change-request already exists")
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "--- PASS: change-request %s\n", result.URL)
		return nil
	},
}

var webhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Convert issue webhooks into FactoryTasks",
	Long:  `Webhook commands receive GitHub or GitLab issue events and render FactoryTask resources.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var webhookOptions struct {
	provider                  string
	namespace                 string
	agent                     string
	agentCommand              string
	agentEnv                  []string
	promptRef                 string
	sandboxTemplateRef        string
	containerName             string
	reportingMode             string
	command                   []string
	triggerAction             []string
	requireLabel              []string
	repository                []string
	changeRequest             bool
	changeRequestPushOnly     bool
	changeRequestAuthTokenEnv string
}

var webhookRenderCmd = &cobra.Command{
	Use:   "render [payload-file]",
	Short: "Render a FactoryTask from a GitHub or GitLab issue webhook payload",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := readPayload(args[0])
		if err != nil {
			return err
		}
		task, err := taskpkg.FactoryTaskFromIssueWebhook(payload, issueWebhookOptions())
		if err != nil {
			if ignored, ok := err.(*taskpkg.IgnoredIssueWebhookError); ok {
				fmt.Fprintf(cmd.OutOrStdout(), "ignored=true reason=%q\n", ignored.Reason)
				return nil
			}
			return err
		}
		data, err := taskpkg.FactoryTaskYAML(task)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	},
}

var webhookServeOptions struct {
	addr         string
	secret       string
	apply        bool
	responseYAML bool
}

var webhookServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve GitHub and GitLab issue webhook endpoints",
	RunE: func(cmd *cobra.Command, args []string) error {
		mux := http.NewServeMux()
		mux.HandleFunc("/webhook/github", issueWebhookHandler(cmd, taskpkg.ProviderGitHub))
		mux.HandleFunc("/webhook/gitlab", issueWebhookHandler(cmd, taskpkg.ProviderGitLab))
		fmt.Fprintf(cmd.OutOrStdout(), "listening on %s\n", webhookServeOptions.addr)
		return http.ListenAndServe(webhookServeOptions.addr, mux)
	},
}

var webhookTriggerPipelineOptions struct {
	addr     string
	secret   string
	tokenEnv string
	ref      string
	apiBase  string
}

var webhookTriggerPipelineCmd = &cobra.Command{
	Use:   "trigger-pipeline",
	Short: "Trigger a GitLab CI pipeline from an issue webhook",
	RunE: func(cmd *cobra.Command, args []string) error {
		mux := http.NewServeMux()
		mux.HandleFunc("/webhook/gitlab", gitLabPipelineTriggerHandler(cmd))
		fmt.Fprintf(cmd.OutOrStdout(), "listening on %s\n", webhookTriggerPipelineOptions.addr)
		return http.ListenAndServe(webhookTriggerPipelineOptions.addr, mux)
	},
}

var manifestCmd = &cobra.Command{
	Use:   "manifest [file]",
	Short: "Render the SandboxClaim manifest a FactoryTask would create",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task, err := readTask(args[0])
		if err != nil {
			return err
		}
		output, err := taskpkg.Reconcile(task)
		if err != nil {
			return err
		}
		data, err := output.SandboxClaimYAML()
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	},
}

var runOnceOptions struct {
	timeout             time.Duration
	report              bool
	createChangeRequest bool
}

var watchOptions struct {
	namespace           string
	interval            time.Duration
	timeout             time.Duration
	once                bool
	retryFailed         bool
	report              bool
	createChangeRequest bool
}

var patchStatusOptions struct {
	namespace        string
	phase            string
	message          string
	reason           string
	sandboxClaimName string
	sandboxName      string
	lastResultURL    string
	observedCommit   string
}

var runOnceCmd = &cobra.Command{
	Use:   "run-once [file]",
	Short: "Create a SandboxClaim and execute a FactoryTask once with kubectl",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskData, task, err := readTaskWithData(args[0])
		if err != nil {
			return err
		}
		return executeTask(cmd.OutOrStdout(), task, taskData, true, runOnceOptions.timeout, "run-once")
	},
}

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Continuously reconcile pending FactoryTasks with kubectl",
	RunE: func(cmd *cobra.Command, args []string) error {
		namespace := watchOptions.namespace
		if namespace == "" {
			namespace = "default"
		}
		for {
			tasks, err := listFactoryTasks(namespace)
			if err != nil {
				return err
			}
			for i := range tasks {
				task := tasks[i]
				if !shouldReconcile(task, watchOptions.retryFailed) {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "--- RECONCILE: %s/%s\n", namespaceForTask(&task), task.Metadata.Name)
				if err := executeTask(cmd.OutOrStdout(), &task, nil, false, watchOptions.timeout, "watch-controller"); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "FactoryTask %s/%s failed: %v\n", namespaceForTask(&task), task.Metadata.Name, err)
				}
			}
			if watchOptions.once {
				return nil
			}
			time.Sleep(watchOptions.interval)
		}
	},
}

var patchStatusCmd = &cobra.Command{
	Use:   "patch-status [task-name]",
	Short: "Patch the status subresource of a FactoryTask",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		namespace := patchStatusOptions.namespace
		if namespace == "" {
			namespace = "default"
		}
		return patchTaskStatus(namespace, args[0], taskpkg.StatusPatchOptions{
			Phase:            patchStatusOptions.phase,
			Reason:           patchStatusOptions.reason,
			Message:          patchStatusOptions.message,
			SandboxClaimName: patchStatusOptions.sandboxClaimName,
			SandboxName:      patchStatusOptions.sandboxName,
			LastResultURL:    patchStatusOptions.lastResultURL,
			ObservedCommit:   patchStatusOptions.observedCommit,
		})
	},
}

func init() {
	Cmd.AddCommand(validateCmd)
	Cmd.AddCommand(planCmd)
	Cmd.AddCommand(planDryRunCmd)
	Cmd.AddCommand(reportCmd)
	Cmd.AddCommand(changeRequestCmd)
	Cmd.AddCommand(webhookCmd)
	Cmd.AddCommand(controllerCmd)
	reportCmd.AddCommand(reportCommentCmd)
	changeRequestCmd.AddCommand(changeRequestCreateCmd)
	webhookCmd.AddCommand(webhookRenderCmd)
	webhookCmd.AddCommand(webhookServeCmd)
	webhookCmd.AddCommand(webhookTriggerPipelineCmd)
	controllerCmd.AddCommand(manifestCmd)
	controllerCmd.AddCommand(runOnceCmd)
	controllerCmd.AddCommand(watchCmd)
	controllerCmd.AddCommand(patchStatusCmd)
	runOnceCmd.Flags().DurationVar(&runOnceOptions.timeout, "timeout", 5*time.Minute, "time to wait for the SandboxClaim to become Ready")
	runOnceCmd.Flags().BoolVar(&runOnceOptions.report, "report", true, "write reporting.comment results when spec.reporting is configured")
	runOnceCmd.Flags().BoolVar(&runOnceOptions.createChangeRequest, "create-change-request", true, "create PR/MR when spec.changeRequest is configured")
	reportCommentCmd.Flags().StringVar(&reportCommentOptions.message, "message", "", "comment body to write")
	reportCommentCmd.Flags().StringVar(&reportCommentOptions.token, "token", "", "provider API token; defaults to GITHUB_TOKEN or GITLAB_TOKEN")
	reportCommentCmd.Flags().StringVar(&reportCommentOptions.apiBase, "api-base", "", "override provider API base URL")
	reportCommentCmd.Flags().BoolVar(&reportCommentOptions.dryRun, "dry-run", false, "print the comment request without sending it")
	changeRequestCreateCmd.Flags().StringVar(&changeRequestCreateOptions.token, "token", "", "provider API token; defaults to GITHUB_TOKEN or GITLAB_TOKEN")
	changeRequestCreateCmd.Flags().StringVar(&changeRequestCreateOptions.apiBase, "api-base", "", "override provider API base URL")
	changeRequestCreateCmd.Flags().BoolVar(&changeRequestCreateOptions.dryRun, "dry-run", false, "print the PR/MR request without sending it")
	webhookCmd.PersistentFlags().StringVarP(&webhookOptions.namespace, "namespace", "n", "default", "FactoryTask namespace")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.agent, "agent", "builder", "agent name for generated FactoryTasks")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.agentCommand, "agent-command", "ai-factory-agent openai-compatible", "agent runner command for generated FactoryTasks")
	webhookCmd.PersistentFlags().StringArrayVar(&webhookOptions.agentEnv, "agent-env", nil, "environment variable to inject into the agent sandbox; can be repeated")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.promptRef, "prompt-ref", "", "agent prompt reference")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.sandboxTemplateRef, "sandbox-template", "go-dev", "sandbox template reference")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.containerName, "container", "", "sandbox container name")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.reportingMode, "reporting-mode", "comment", "reporting mode for generated FactoryTasks")
	webhookCmd.PersistentFlags().StringArrayVar(&webhookOptions.command, "command", nil, "command to run in the generated FactoryTask; can be repeated")
	webhookCmd.PersistentFlags().StringArrayVar(&webhookOptions.triggerAction, "trigger-action", nil, "issue action that can trigger a FactoryTask; can be repeated")
	webhookCmd.PersistentFlags().StringArrayVar(&webhookOptions.requireLabel, "require-label", nil, "issue label required to trigger a FactoryTask; can be repeated")
	webhookCmd.PersistentFlags().StringArrayVar(&webhookOptions.repository, "repository", nil, "repository allowed to trigger FactoryTasks; can be repeated")
	webhookCmd.PersistentFlags().BoolVar(&webhookOptions.changeRequest, "change-request", true, "enable branch, commit, push, and PR/MR creation for generated FactoryTasks")
	webhookCmd.PersistentFlags().BoolVar(&webhookOptions.changeRequestPushOnly, "change-request-push-only", false, "push changes but skip PR/MR creation (requires --change-request=true)")
	webhookCmd.PersistentFlags().StringVar(&webhookOptions.changeRequestAuthTokenEnv, "change-request-auth-token-env", "", "environment variable name for git push and PR/MR creation token")
	webhookRenderCmd.Flags().StringVar(&webhookOptions.provider, "provider", taskpkg.ProviderGitHub, "webhook provider: github or gitlab")
	webhookServeCmd.Flags().StringVar(&webhookServeOptions.addr, "addr", ":8080", "listen address")
	webhookServeCmd.Flags().StringVar(&webhookServeOptions.secret, "secret", "", "webhook secret for GitHub signatures or GitLab tokens")
	webhookServeCmd.Flags().BoolVar(&webhookServeOptions.apply, "apply", false, "apply generated FactoryTasks with kubectl")
	webhookServeCmd.Flags().BoolVar(&webhookServeOptions.responseYAML, "response-yaml", false, "write generated FactoryTask YAML in HTTP responses")
	webhookTriggerPipelineCmd.Flags().StringVar(&webhookTriggerPipelineOptions.addr, "addr", ":8080", "listen address")
	webhookTriggerPipelineCmd.Flags().StringVar(&webhookTriggerPipelineOptions.secret, "secret", "", "GitLab webhook token")
	webhookTriggerPipelineCmd.Flags().StringVar(&webhookTriggerPipelineOptions.tokenEnv, "token-env", "GITLAB_TOKEN", "environment variable containing the GitLab API token")
	webhookTriggerPipelineCmd.Flags().StringVar(&webhookTriggerPipelineOptions.ref, "ref", "", "pipeline ref; defaults to the issue repository default branch")
	webhookTriggerPipelineCmd.Flags().StringVar(&webhookTriggerPipelineOptions.apiBase, "api-base", "", "GitLab API base URL; defaults to https://<host>/api/v4")
	watchCmd.Flags().StringVarP(&watchOptions.namespace, "namespace", "n", "default", "FactoryTask namespace")
	watchCmd.Flags().DurationVar(&watchOptions.interval, "interval", 15*time.Second, "time between FactoryTask list polls")
	watchCmd.Flags().DurationVar(&watchOptions.timeout, "timeout", 5*time.Minute, "time to wait for each SandboxClaim to become Ready")
	watchCmd.Flags().BoolVar(&watchOptions.once, "once", false, "list and reconcile once, then exit")
	watchCmd.Flags().BoolVar(&watchOptions.retryFailed, "retry-failed", false, "reconcile FactoryTasks whose phase is Failed")
	watchCmd.Flags().BoolVar(&watchOptions.report, "report", true, "write reporting.comment results when spec.reporting is configured")
	watchCmd.Flags().BoolVar(&watchOptions.createChangeRequest, "create-change-request", true, "create PR/MR when spec.changeRequest is configured")
	patchStatusCmd.Flags().StringVarP(&patchStatusOptions.namespace, "namespace", "n", "default", "FactoryTask namespace")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.phase, "phase", taskpkg.PhasePending, "FactoryTask phase")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.message, "message", "", "status message")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.reason, "reason", "ManualPatch", "condition reason")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.sandboxClaimName, "sandbox-claim-name", "", "SandboxClaim name")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.sandboxName, "sandbox-name", "", "sandbox name")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.lastResultURL, "last-result-url", "", "URL for the latest result")
	patchStatusCmd.Flags().StringVar(&patchStatusOptions.observedCommit, "observed-commit", "", "observed source commit")
}

func readTask(path string) (*taskpkg.FactoryTask, error) {
	_, task, err := readTaskWithData(path)
	return task, err
}

func readTaskWithData(path string) ([]byte, *taskpkg.FactoryTask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read task file: %w", err)
	}
	task, err := taskpkg.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	return data, task, nil
}

func readPayload(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read webhook payload: %w", err)
	}
	return data, nil
}

func issueWebhookOptions() taskpkg.IssueWebhookOptions {
	return taskpkg.IssueWebhookOptions{
		Provider:                  webhookOptions.provider,
		Namespace:                 webhookOptions.namespace,
		AgentName:                 webhookOptions.agent,
		AgentCommand:              webhookOptions.agentCommand,
		AgentEnv:                  webhookOptions.agentEnv,
		PromptRef:                 webhookOptions.promptRef,
		SandboxTemplateRef:        webhookOptions.sandboxTemplateRef,
		ContainerName:             webhookOptions.containerName,
		ReportingMode:             webhookOptions.reportingMode,
		Commands:                  webhookOptions.command,
		TriggerActions:            webhookOptions.triggerAction,
		RequiredLabels:            webhookOptions.requireLabel,
		Repositories:              webhookOptions.repository,
		ChangeRequestEnabled:      webhookOptions.changeRequest,
		ChangeRequestPushOnly:     webhookOptions.changeRequestPushOnly,
		ChangeRequestAuthTokenEnv: webhookOptions.changeRequestAuthTokenEnv,
	}
}

func issueWebhookHandler(cmd *cobra.Command, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
			return
		}
		if err := verifyIssueWebhookRequest(provider, body, req); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		opts := issueWebhookOptions()
		opts.Provider = provider
		task, err := taskpkg.FactoryTaskFromIssueWebhook(body, opts)
		if err != nil {
			if ignored, ok := err.(*taskpkg.IgnoredIssueWebhookError); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprintf(w, `{"ignored":true,"reason":%q}`+"\n", ignored.Reason)
				fmt.Fprintf(cmd.OutOrStdout(), "--- WEBHOOK IGNORED: %s %s\n", provider, ignored.Reason)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := taskpkg.FactoryTaskYAML(task)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if webhookServeOptions.apply {
			if err := runKubectlWithInput(data, "apply", "-f", "-"); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "--- WEBHOOK: %s issue %s -> FactoryTask %s/%s\n", provider, task.Spec.Trigger.ID, namespaceForTask(task), task.Metadata.Name)
		if webhookServeOptions.responseYAML {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"triggered":true,"task":"%s","namespace":"%s","applied":%t}`+"\n", task.Metadata.Name, namespaceForTask(task), webhookServeOptions.apply)
	}
}

func verifyIssueWebhookRequest(provider string, body []byte, req *http.Request) error {
	switch provider {
	case taskpkg.ProviderGitHub:
		return taskpkg.VerifyGitHubWebhookSignature(webhookServeOptions.secret, body, req.Header.Get("X-Hub-Signature-256"))
	case taskpkg.ProviderGitLab:
		return taskpkg.VerifyGitLabWebhookToken(webhookServeOptions.secret, req.Header.Get("X-Gitlab-Token"))
	default:
		return fmt.Errorf("unsupported webhook provider %q", provider)
	}
}

func gitLabPipelineTriggerHandler(cmd *cobra.Command) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
			return
		}
		if err := taskpkg.VerifyGitLabWebhookToken(webhookTriggerPipelineOptions.secret, req.Header.Get("X-Gitlab-Token")); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		event, err := taskpkg.ParseIssueWebhook(body, taskpkg.ProviderGitLab)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		opts := issueWebhookOptions()
		opts.Provider = taskpkg.ProviderGitLab
		if len(opts.RequiredLabels) == 0 {
			opts.RequiredLabels = []string{"ai-factory-run"}
		}
		if ok, reason := taskpkg.ShouldTriggerIssue(event, opts); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"ignored":true,"reason":%q}`+"\n", reason)
			fmt.Fprintf(cmd.OutOrStdout(), "--- PIPELINE IGNORED: gitlab %s\n", reason)
			return
		}

		tokenEnv := webhookTriggerPipelineOptions.tokenEnv
		if tokenEnv == "" {
			tokenEnv = "GITLAB_TOKEN"
		}
		token := os.Getenv(tokenEnv)
		if token == "" {
			http.Error(w, fmt.Sprintf("%s is required to trigger a GitLab pipeline", tokenEnv), http.StatusInternalServerError)
			return
		}

		apiBase := webhookTriggerPipelineOptions.apiBase
		if apiBase == "" {
			apiBase = fmt.Sprintf("https://%s/api/v4", event.RepositoryHost)
		}
		ref := webhookTriggerPipelineOptions.ref
		if ref == "" {
			ref = event.DefaultBranch
		}
		if ref == "" {
			ref = "main"
		}
		form := url.Values{}
		form.Set("ref", ref)
		form.Set("variables[AI_FACTORY_ISSUE_IID]", strconv.Itoa(event.IssueNumber))
		form.Set("variables[AI_FACTORY_ISSUE_ACTION]", event.Action)
		form.Set("variables[AI_FACTORY_ISSUE_URL]", event.IssueURL)
		form.Set("variables[AI_FACTORY_TRIGGER_LABEL]", opts.RequiredLabels[0])
		endpoint := fmt.Sprintf("%s/projects/%s/pipeline",
			strings.TrimRight(apiBase, "/"), url.PathEscape(event.Repository))
		pipelineReq, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			http.Error(w, fmt.Sprintf("create GitLab pipeline request: %v", err), http.StatusInternalServerError)
			return
		}
		pipelineReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		pipelineReq.Header.Set("PRIVATE-TOKEN", token)
		resp, err := http.DefaultClient.Do(pipelineReq)
		if err != nil {
			http.Error(w, fmt.Sprintf("trigger GitLab pipeline: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read GitLab pipeline response: %v", err), http.StatusBadGateway)
			return
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			http.Error(w, fmt.Sprintf("GitLab pipeline returned %s: %s", resp.Status, strings.TrimSpace(string(responseBody))), http.StatusBadGateway)
			return
		}

		var pipeline struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		}
		_ = json.Unmarshal(responseBody, &pipeline)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"triggered":true,"project":%q,"issue":%d,"pipeline":%d,"status":%q,"web_url":%q}`+"\n",
			event.Repository, event.IssueNumber, pipeline.ID, pipeline.Status, pipeline.WebURL)
		fmt.Fprintf(cmd.OutOrStdout(), "--- PIPELINE: gitlab %s issue #%d -> pipeline %d\n",
			event.Repository, event.IssueNumber, pipeline.ID)
	}
}

func patchTaskStatus(namespace, name string, opts taskpkg.StatusPatchOptions) error {
	patch, err := taskpkg.StatusMergePatch(opts)
	if err != nil {
		return err
	}
	return runKubectl(nil, "patch", "factorytask", name, "-n", namespace, "--type=merge", "--subresource=status", "-p", string(patch))
}

// cleanupSandboxClaim deletes the SandboxClaim resource. Called when a task reaches terminal state.
func cleanupSandboxClaim(namespace, name string) {
	_ = runKubectl(nil, "delete", "sandboxclaim", name, "-n", namespace, "--ignore-not-found")
}

func executeTask(out io.Writer, task *taskpkg.FactoryTask, taskData []byte, applyTask bool, timeout time.Duration, controllerName string) error {
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
	defer cleanupSandboxClaim(namespace, claim)
	if applyTask {
		if err := runKubectlWithInput(taskData, "apply", "-f", "-"); err != nil {
			return err
		}
	}
	if err := patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
		Phase:            taskpkg.PhasePending,
		Reason:           "TaskAccepted",
		Message:          fmt.Sprintf("FactoryTask accepted by %s", controllerName),
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
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("SandboxClaim apply failed: %v", err), reportingEnabled(controllerName))
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
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("SandboxClaim ready wait failed: %v", err), reportingEnabled(controllerName))
		return err
	}

	sandboxName, err := kubectlOutput("get", "sandboxclaim", claim, "-n", namespace, "-o", "jsonpath={.status.sandbox.name}")
	if err != nil {
		return err
	}
	sandboxName = strings.TrimSpace(sandboxName)
	if sandboxName == "" {
		return fmt.Errorf("sandboxclaim %s/%s is ready but status.sandboxName is empty", namespace, claim)
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
			reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("%s failed: %v", step.Name, err), reportingEnabled(controllerName))
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
	wantsChangeRequest := changeRequestEnabled(controllerName)
	resultURL, changeRequestAlreadyExists, err := createTaskChangeRequest(task, wantsChangeRequest)
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
		reportTaskResult(out, task, taskpkg.PhaseFailed, fmt.Sprintf("Change request creation failed: %v", err), reportingEnabled(controllerName))
		return err
	}
	if err := validateChangeRequestResult(task, wantsChangeRequest, resultURL, changeRequestAlreadyExists); err != nil {
		failure := taskpkg.ClassifyFailure(err.Error())
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase:            taskpkg.PhaseFailed,
			Reason:           "NoChangeRequest",
			Message:          err.Error(),
			SandboxClaimName: claim,
			SandboxName:      sandboxName,
			FailureReason:    failure,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, err.Error(), reportingEnabled(controllerName))
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
	reportTaskResult(out, task, taskpkg.PhaseSucceeded, resultMessage, reportingEnabled(controllerName))
	fmt.Fprintln(out, "PASS")
	return nil
}

func reportingEnabled(controllerName string) bool {
	switch controllerName {
	case "run-once":
		return runOnceOptions.report
	case "watch-controller":
		return watchOptions.report
	default:
		return true
	}
}

func changeRequestEnabled(controllerName string) bool {
	switch controllerName {
	case "run-once":
		return runOnceOptions.createChangeRequest
	case "watch-controller":
		return watchOptions.createChangeRequest
	default:
		return true
	}
}

func createTaskChangeRequest(task *taskpkg.FactoryTask, enabled bool) (string, bool, error) {
	if !enabled || !task.Spec.ChangeRequest.Enabled {
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

func validateChangeRequestResult(task *taskpkg.FactoryTask, enabled bool, resultURL string, alreadyExists bool) error {
	if !enabled || !task.Spec.ChangeRequest.Enabled || task.Spec.ChangeRequest.PushOnly || alreadyExists || strings.TrimSpace(resultURL) != "" {
		return nil
	}
	return fmt.Errorf("no change request created: provider returned no change request URL")
}

func reportTaskResult(out io.Writer, task *taskpkg.FactoryTask, phase, message string, enabled bool) {
	if !enabled || task.Spec.Reporting.Mode != "comment" || task.Spec.Reporting.TargetURL == "" {
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

func buildReportMessageFromString(task *taskpkg.FactoryTask, phase, message string) string {
	return buildReportMessage(task, phase, taskpkg.ClassifyFailure(message))
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

type factoryTaskList struct {
	Items []taskpkg.FactoryTask `json:"items"`
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

func shouldReconcile(task taskpkg.FactoryTask, retryFailed bool) bool {
	switch task.Status.Phase {
	case "", taskpkg.PhasePending, taskpkg.PhaseClaimCreated, taskpkg.PhaseSandboxReady:
		return true
	case taskpkg.PhaseFailed:
		return retryFailed
	default:
		return false
	}
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
	} {
		if secret := os.Getenv(name); secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted:"+name+">")
		}
	}
	return redacted
}
