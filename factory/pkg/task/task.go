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

// Package task defines the provider-neutral task contract used by AI Factory.
package task

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	// Kind is the Kubernetes kind for a factory task.
	Kind = "FactoryTask"
	// APIVersion is the Kubernetes API version for a factory task.
	APIVersion = "factory.ai.gke.io/v1alpha1"

	// ProviderGitHub identifies GitHub-hosted repositories.
	ProviderGitHub = "github"
	// ProviderGitLab identifies GitLab-hosted repositories.
	ProviderGitLab = "gitlab"
)

// FactoryTask is the provider-neutral description of a coding-agent task.
type FactoryTask struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ObjectMeta        `yaml:"metadata"`
	Spec       FactoryTaskSpec   `yaml:"spec"`
	Status     FactoryTaskStatus `yaml:"status,omitempty"`
}

// ObjectMeta contains the metadata subset needed by this package.
type ObjectMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

// FactoryTaskSpec describes where code lives, what should run, and where
// results should be reported.
type FactoryTaskSpec struct {
	Source        SourceSpec        `yaml:"source"`
	Trigger       TriggerSpec       `yaml:"trigger,omitempty"`
	Agent         AgentSpec         `yaml:"agent"`
	Sandbox       SandboxSpec       `yaml:"sandbox"`
	Work          WorkSpec          `yaml:"work"`
	ChangeRequest ChangeRequestSpec `yaml:"changeRequest,omitempty"`
	Reporting     ReportingSpec     `yaml:"reporting,omitempty"`
}

// SourceSpec identifies a repository on GitHub, GitLab, or compatible hosts.
type SourceSpec struct {
	Provider   string `yaml:"provider"`
	Host       string `yaml:"host,omitempty"`
	Repository string `yaml:"repository"`
	BaseRef    string `yaml:"baseRef"`
	CloneURL   string `yaml:"cloneURL,omitempty"`
}

// TriggerSpec records the event that created this task.
type TriggerSpec struct {
	Type  string `yaml:"type,omitempty"`
	ID    string `yaml:"id,omitempty"`
	URL   string `yaml:"url,omitempty"`
	Actor string `yaml:"actor,omitempty"`
}

// Agent workflow modes select how much of the end-to-end flow the agent owns.
const (
	// AgentWorkflowScripted (default, also the zero value "") keeps the current
	// behavior: the agent produces a shell script and the controller performs
	// git commit/push, change-request creation, and CI repair.
	AgentWorkflowScripted = "scripted"
	// AgentWorkflowDelegated hands the full workflow to the agent (e.g. Codex +
	// skill): edit, run checks locally, commit, push, and open the PR/MR itself.
	// The controller becomes a thin launcher and only reports the result.
	AgentWorkflowDelegated = "delegated"
)

// AgentSpec selects the agent persona and optional prompt/config reference.
type AgentSpec struct {
	Name      string   `yaml:"name"`
	PromptRef string   `yaml:"promptRef,omitempty"`
	Command   string   `yaml:"command,omitempty"`
	Workflow  string   `yaml:"workflow,omitempty"`
	Env       []string `yaml:"env,omitempty"`
}

// IsDelegated reports whether the agent owns the full workflow (git/PR/CI),
// so the controller must skip its own change-request and CI-repair steps.
func (s AgentSpec) IsDelegated() bool {
	return strings.EqualFold(strings.TrimSpace(s.Workflow), AgentWorkflowDelegated)
}

// SandboxSpec describes the sandbox that should execute the task.
type SandboxSpec struct {
	TemplateRef   string `yaml:"templateRef"`
	ClaimName     string `yaml:"claimName,omitempty"`
	ContainerName string `yaml:"containerName,omitempty"`
}

// WorkSpec describes the actual coding task.
type WorkSpec struct {
	Instructions string   `yaml:"instructions"`
	Commands     []string `yaml:"commands,omitempty"`
}

// ChangeRequestSpec describes branch, commit, and push behavior for code changes.
type ChangeRequestSpec struct {
	Enabled       bool   `yaml:"enabled,omitempty"`
	PushOnly      bool   `yaml:"pushOnly,omitempty"`
	BranchName    string `yaml:"branchName,omitempty"`
	BranchPrefix  string `yaml:"branchPrefix,omitempty"`
	TargetBranch  string `yaml:"targetBranch,omitempty"`
	CommitMessage string `yaml:"commitMessage,omitempty"`
	AuthorName    string `yaml:"authorName,omitempty"`
	AuthorEmail   string `yaml:"authorEmail,omitempty"`
	Title         string `yaml:"title,omitempty"`
	Body          string `yaml:"body,omitempty"`
	RemoteName    string `yaml:"remoteName,omitempty"`
	AuthTokenEnv  string `yaml:"authTokenEnv,omitempty"`
	AuthUsername  string `yaml:"authUsername,omitempty"`
	ForkOwner     string `yaml:"forkOwner,omitempty"`
}

// ReportingSpec describes how execution results should be reported.
type ReportingSpec struct {
	Provider  string `yaml:"provider,omitempty"`
	Mode      string `yaml:"mode,omitempty"`
	TargetURL string `yaml:"targetURL,omitempty"`
}

// FactoryTaskStatus captures the controller-visible progress of a task.
type FactoryTaskStatus struct {
	Phase            string      `yaml:"phase,omitempty" json:"phase,omitempty"`
	Conditions       []Condition `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	SandboxClaimName string      `yaml:"sandboxClaimName,omitempty" json:"sandboxClaimName,omitempty"`
	SandboxName      string      `yaml:"sandboxName,omitempty" json:"sandboxName,omitempty"`
	StartedAt        string      `yaml:"startedAt,omitempty" json:"startedAt,omitempty"`
	CompletedAt      string      `yaml:"completedAt,omitempty" json:"completedAt,omitempty"`
	LastResultURL    string      `yaml:"lastResultURL,omitempty" json:"lastResultURL,omitempty"`
	LastMessage      string      `yaml:"lastMessage,omitempty" json:"lastMessage,omitempty"`
	ObservedCommit   string      `yaml:"observedCommit,omitempty" json:"observedCommit,omitempty"`
}

// Condition describes one status transition for a FactoryTask.
type Condition struct {
	Type               string `yaml:"type" json:"type"`
	Status             string `yaml:"status" json:"status"`
	Reason             string `yaml:"reason,omitempty" json:"reason,omitempty"`
	Message            string `yaml:"message,omitempty" json:"message,omitempty"`
	LastTransitionTime string `yaml:"lastTransitionTime,omitempty" json:"lastTransitionTime,omitempty"`
}

// Parse decodes a FactoryTask from YAML.
func Parse(data []byte) (*FactoryTask, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.SetStrict(true)

	var task FactoryTask
	if err := decoder.Decode(&task); err != nil {
		return nil, fmt.Errorf("decode FactoryTask: %w", err)
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	return &task, nil
}

// Validate checks the provider-neutral task contract.
func (t *FactoryTask) Validate() error {
	var errs []error

	if t.APIVersion != APIVersion {
		errs = append(errs, fmt.Errorf("apiVersion must be %q", APIVersion))
	}
	if t.Kind != Kind {
		errs = append(errs, fmt.Errorf("kind must be %q", Kind))
	}
	if strings.TrimSpace(t.Metadata.Name) == "" {
		errs = append(errs, errors.New("metadata.name is required"))
	}

	errs = append(errs, t.Spec.validate()...)
	return errors.Join(errs...)
}

func (s FactoryTaskSpec) validate() []error {
	var errs []error
	errs = append(errs, s.Source.validate()...)
	if strings.TrimSpace(s.Agent.Name) == "" {
		errs = append(errs, errors.New("spec.agent.name is required"))
	}
	for _, envName := range s.Agent.Env {
		if !isShellEnvName(envName) {
			errs = append(errs, errors.New("spec.agent.env entries must be valid shell environment variable names"))
			break
		}
	}
	switch strings.TrimSpace(s.Agent.Workflow) {
	case "", AgentWorkflowScripted, AgentWorkflowDelegated:
	default:
		errs = append(errs, fmt.Errorf("spec.agent.workflow must be %q or %q", AgentWorkflowScripted, AgentWorkflowDelegated))
	}
	if strings.TrimSpace(s.Sandbox.TemplateRef) == "" {
		errs = append(errs, errors.New("spec.sandbox.templateRef is required"))
	}
	if strings.TrimSpace(s.Work.Instructions) == "" && len(s.Work.Commands) == 0 {
		errs = append(errs, errors.New("spec.work.instructions or spec.work.commands is required"))
	}
	errs = append(errs, s.ChangeRequest.validate()...)
	if s.ChangeRequest.ForkOwner != "" {
		if s.Source.Provider == ProviderGitLab {
			errs = append(errs, errors.New("spec.changeRequest.forkOwner is only supported for the github provider"))
		}
		if strings.ContainsAny(s.ChangeRequest.ForkOwner, "/: \t") {
			errs = append(errs, errors.New("spec.changeRequest.forkOwner must be a GitHub owner name"))
		}
	}
	if s.Reporting.TargetURL != "" {
		if _, err := url.ParseRequestURI(s.Reporting.TargetURL); err != nil {
			errs = append(errs, fmt.Errorf("spec.reporting.targetURL must be a valid URL: %w", err))
		}
	}
	switch s.Reporting.Provider {
	case "", ProviderGitHub, ProviderGitLab:
	default:
		errs = append(errs, fmt.Errorf("spec.reporting.provider must be %q or %q", ProviderGitHub, ProviderGitLab))
	}
	return errs
}

func (s ChangeRequestSpec) validate() []error {
	if !s.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(s.BranchName) != "" && strings.TrimSpace(s.BranchPrefix) != "" {
		errs = append(errs, errors.New("spec.changeRequest.branchName and spec.changeRequest.branchPrefix are mutually exclusive"))
	}
	if strings.TrimSpace(s.AuthTokenEnv) != "" && !isShellEnvName(s.AuthTokenEnv) {
		errs = append(errs, errors.New("spec.changeRequest.authTokenEnv must be a valid shell environment variable name"))
	}
	return errs
}

func isShellEnvName(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (s SourceSpec) validate() []error {
	var errs []error
	switch s.Provider {
	case ProviderGitHub, ProviderGitLab:
	case "":
		errs = append(errs, errors.New("spec.source.provider is required"))
	default:
		errs = append(errs, fmt.Errorf("spec.source.provider must be %q or %q", ProviderGitHub, ProviderGitLab))
	}
	if strings.TrimSpace(s.Repository) == "" {
		errs = append(errs, errors.New("spec.source.repository is required"))
	}
	if strings.TrimSpace(s.BaseRef) == "" {
		errs = append(errs, errors.New("spec.source.baseRef is required"))
	}
	if s.CloneURL != "" {
		u, err := url.Parse(s.CloneURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, errors.New("spec.source.cloneURL must be an absolute URL"))
		}
	}
	return errs
}
