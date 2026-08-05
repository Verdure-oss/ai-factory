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

package task

import (
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	defaultNamespace = "default"

	sandboxClaimAPIVersion = "extensions.agents.x-k8s.io/v1beta1"
	sandboxClaimKind       = "SandboxClaim"
	taskNameLabel          = "factory.ai.gke.io/task"
	taskProviderLabel      = "factory.ai.gke.io/provider"
)

var dnsLabelUnsafe = regexp.MustCompile(`[^a-z0-9.-]+`)

// ReconcileOutput is the Kubernetes work a FactoryTask controller should create.
type ReconcileOutput struct {
	Plan         *ExecutionPlan
	SandboxClaim KubernetesObject
}

// KubernetesObject is a small generic Kubernetes manifest representation.
type KubernetesObject struct {
	APIVersion string                 `json:"apiVersion" yaml:"apiVersion"`
	Kind       string                 `json:"kind" yaml:"kind"`
	Metadata   KubernetesMetadata     `json:"metadata" yaml:"metadata"`
	Spec       map[string]interface{} `json:"spec" yaml:"spec"`
}

// KubernetesMetadata contains the metadata needed for generated resources.
type KubernetesMetadata struct {
	Name      string            `json:"name" yaml:"name"`
	Namespace string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// Reconcile builds the Kubernetes resources needed to execute a FactoryTask.
func Reconcile(task *FactoryTask) (*ReconcileOutput, error) {
	plan, err := BuildExecutionPlan(task)
	if err != nil {
		return nil, err
	}

	namespace := task.Metadata.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	claim := KubernetesObject{
		APIVersion: sandboxClaimAPIVersion,
		Kind:       sandboxClaimKind,
		Metadata: KubernetesMetadata{
			Name:      dnsLabel(plan.SandboxClaim),
			Namespace: namespace,
			Labels: map[string]string{
				taskNameLabel:     dnsLabel(task.Metadata.Name),
				taskProviderLabel: dnsLabel(task.Spec.Source.Provider),
			},
		},
		Spec: map[string]interface{}{
			"warmPoolRef": map[string]interface{}{
				"name": plan.SandboxTemplate,
			},
			"lifecycle": map[string]interface{}{
				"shutdownPolicy":        "Delete",
				"ttlSecondsAfterFinished": 300,
			},
		},
	}
	if task.Spec.ChangeRequest.Enabled && plan.GitAuthTokenEnv != "" {
		if token := ReadConfig(plan.GitAuthTokenEnv); token != "" {
			appendSandboxEnv(claim.Spec, plan.ContainerName, plan.GitAuthTokenEnv, token)
		}
	}
	for _, envName := range task.Spec.Agent.Env {
		if value := ReadConfig(envName); value != "" {
			appendSandboxEnv(claim.Spec, plan.ContainerName, envName, value)
		}
	}
	// Inject git proxy if configured (used by plan step "configure git proxy")
	if proxy := ReadConfig("AI_FACTORY_GIT_PROXY"); proxy != "" {
		appendSandboxEnv(claim.Spec, plan.ContainerName, "AI_FACTORY_GIT_PROXY", proxy)
	}

	return &ReconcileOutput{
		Plan:         plan,
		SandboxClaim: claim,
	}, nil
}

func appendSandboxEnv(spec map[string]interface{}, containerName, name, value string) {
	env := map[string]interface{}{
		"name":  name,
		"value": value,
	}
	if containerName != "" {
		env["containerName"] = containerName
	}
	envs, _ := spec["env"].([]interface{})
	spec["env"] = append(envs, env)
}

// SandboxClaimYAML renders the generated SandboxClaim manifest.
func (o *ReconcileOutput) SandboxClaimYAML() ([]byte, error) {
	if o == nil {
		return nil, fmt.Errorf("reconcile output is nil")
	}
	return yaml.Marshal(o.SandboxClaim)
}

func dnsLabel(value string) string {
	value = strings.ToLower(value)
	value = dnsLabelUnsafe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return "factory-task"
	}
	if len(value) <= 63 {
		return value
	}
	return strings.Trim(value[:63], "-.")
}
