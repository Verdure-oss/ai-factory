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

// Package server implements the ai-factory self-hosted service.
package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
)

// Options holds server configuration.
type Options struct {
	Addr                string
	Namespace           string
	WebhookSecret       string
	AgentName           string
	AgentCommand        string
	SmokeAgentCommand   string
	AgentEnv            []string
	SandboxTemplateRef  string
	ContainerName       string
	SmokeCommands       []string
	ValidationCommands  []string
	WatchInterval       time.Duration
	TaskTimeout         time.Duration
	EnableChangeRequest bool
	ReportEnabled       bool
}

// Cmd represents the server command.
var Cmd = &cobra.Command{
	Use:   "server",
	Short: "Run ai-factory self-hosted service",
	Long: `Start the ai-factory service that listens for GitHub/GitLab webhooks
and continuously reconciles FactoryTask resources.`,
	RunE: runServer,
}

var opts Options

func init() {
	Cmd.Flags().StringVar(&opts.Addr, "addr", ":8080", "listen address for webhook server")
	Cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "default", "FactoryTask namespace")
	Cmd.Flags().StringVar(&opts.WebhookSecret, "webhook-secret", "", "webhook secret for GitHub/GitLab signature verification (or set WEBHOOK_SECRET env)")
	Cmd.Flags().StringVar(&opts.AgentName, "agent", "builder", "agent name for generated FactoryTasks")
	Cmd.Flags().StringVar(&opts.AgentCommand, "agent-command", "ai-factory-agent openai-compatible", "agent runner command")
	Cmd.Flags().StringVar(&opts.SmokeAgentCommand, "smoke-agent-command", "cat >/tmp/ai-factory-agent-prompt.txt", "agent command for smoke mode")
	Cmd.Flags().StringArrayVar(&opts.AgentEnv, "agent-env", nil, "environment variable to inject into agent sandbox")
	Cmd.Flags().StringVar(&opts.SandboxTemplateRef, "sandbox-template", "go-dev", "sandbox template reference")
	Cmd.Flags().StringVar(&opts.ContainerName, "container", "", "sandbox container name")
	Cmd.Flags().DurationVar(&opts.WatchInterval, "watch-interval", 15*time.Second, "interval between FactoryTask list polls")
	Cmd.Flags().DurationVar(&opts.TaskTimeout, "task-timeout", 30*time.Minute, "timeout for each SandboxClaim to become Ready")
	Cmd.Flags().StringArrayVar(&opts.SmokeCommands, "smoke-command", nil, "command to run in smoke mode (can be repeated)")
	Cmd.Flags().StringArrayVar(&opts.ValidationCommands, "validation-command", nil, "validation command for run mode (can be repeated)")
	Cmd.Flags().BoolVar(&opts.EnableChangeRequest, "change-request", true, "enable PR/MR creation")
	Cmd.Flags().BoolVar(&opts.ReportEnabled, "report", true, "enable reporting comments")
}

func runServer(cmd *cobra.Command, args []string) error {
	// Resolve webhook secret from flag or env
	secret := opts.WebhookSecret
	if secret == "" {
		secret = os.Getenv("WEBHOOK_SECRET")
	}
	opts.WebhookSecret = secret

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(cmd.ErrOrStderr(), "received signal %v, shutting down...\n", sig)
		cancel()
	}()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// Start webhook server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := startWebhookServer(ctx, cmd); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("webhook server: %w", err)
		}
	}()

	// Start controller loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := startControllerLoop(ctx, cmd); err != nil {
			errCh <- fmt.Errorf("controller: %w", err)
		}
	}()

	// Wait for shutdown or error
	select {
	case err := <-errCh:
		cancel()
		return err
	case <-ctx.Done():
	}

	wg.Wait()
	return nil
}

func startWebhookServer(ctx context.Context, cmd *cobra.Command) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/github", issueWebhookHandler(cmd, taskpkg.ProviderGitHub))
	mux.HandleFunc("/webhook/gitlab", issueWebhookHandler(cmd, taskpkg.ProviderGitLab))
	mux.HandleFunc("/healthz", healthHandler)

	server := &http.Server{
		Addr:    opts.Addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(cmd.ErrOrStderr(), "webhook server listening on %s\n", opts.Addr)
	return server.ListenAndServe()
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok"}`)
}

func issueWebhookHandler(cmd *cobra.Command, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := readBody(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := verifyWebhook(provider, body, req); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		task, err := taskpkg.FactoryTaskFromIssueWebhook(body, webhookOptions(provider))
		if err != nil {
			if ignored, ok := err.(*taskpkg.IgnoredIssueWebhookError); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprintf(w, `{"ignored":true,"reason":%q}`+"\n", ignored.Reason)
				fmt.Fprintf(cmd.ErrOrStderr(), "webhook ignored: %s %s\n", provider, ignored.Reason)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Check if this FactoryTask already exists and is being processed.
		// This prevents duplicate processing when setting labels triggers new webhook events.
		alreadyExists := factoryTaskExists(namespaceForTask(task), task.Metadata.Name)

		data, err := taskpkg.FactoryTaskYAML(task)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := runKubectlWithInput(data, "apply", "-f", "-"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "webhook: %s issue %s -> FactoryTask %s/%s (exists=%v)\n", provider, task.Spec.Trigger.ID, namespaceForTask(task), task.Metadata.Name, alreadyExists)

		// Only set labels on first creation (not on re-trigger)
		if !alreadyExists && provider == taskpkg.ProviderGitHub {
			gh := NewGitHubClient()
			if gh.HasToken() && task.Spec.Trigger.URL != "" {
				repo := task.Spec.Source.Repository
				issueNum := 0
				fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
				if repo != "" && issueNum > 0 {
					_ = gh.SetTaskRunning(req.Context(), repo, issueNum)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"triggered":true,"task":"%s","namespace":"%s","alreadyExists":%v}`+"\n", task.Metadata.Name, namespaceForTask(task), alreadyExists)
	}
}

func webhookOptions(provider string) taskpkg.IssueWebhookOptions {
	return taskpkg.IssueWebhookOptions{
		Provider:             provider,
		Namespace:            opts.Namespace,
		AgentName:            opts.AgentName,
		AgentCommand:         opts.AgentCommand,
		SmokeAgentCommand:    opts.SmokeAgentCommand,
		AgentEnv:             opts.AgentEnv,
		SandboxTemplateRef:   opts.SandboxTemplateRef,
		ContainerName:        opts.ContainerName,
		ReportingMode:        "comment",
		Commands:             opts.ValidationCommands,
		SmokeCommands:        opts.SmokeCommands,
		TriggerActions:       []string{"labeled"},
		RequiredLabels:       []string{"ai-factory-run", "ai-factory-smoke"},
		RequireAllOf:         []string{"ai-factory"},
		ChangeRequestEnabled: opts.EnableChangeRequest,
	}
}

func verifyWebhook(provider string, body []byte, req *http.Request) error {
	switch provider {
	case taskpkg.ProviderGitHub:
		return taskpkg.VerifyGitHubWebhookSignature(opts.WebhookSecret, body, req.Header.Get("X-Hub-Signature-256"))
	case taskpkg.ProviderGitLab:
		return taskpkg.VerifyGitLabWebhookToken(opts.WebhookSecret, req.Header.Get("X-Gitlab-Token"))
	default:
		return fmt.Errorf("unsupported webhook provider %q", provider)
	}
}

func readBody(req *http.Request) ([]byte, error) {
	defer req.Body.Close()
	buf := make([]byte, 0, 1<<20) // 1MB initial capacity
	tmp := make([]byte, 32*1024)
	for {
		n, err := req.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 10<<20 { // 10MB limit
			return nil, fmt.Errorf("request body too large")
		}
	}
	return buf, nil
}
