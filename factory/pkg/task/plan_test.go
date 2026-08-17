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
	"strings"
	"testing"
)

func TestBuildCIRepairScriptEnvInjection(t *testing.T) {
	task := &FactoryTask{
		Metadata: ObjectMeta{Name: "ci-repair-test"},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{
				Provider:   ProviderGitHub,
				Repository: "org/repo",
				BaseRef:    "main",
			},
			Agent: AgentSpec{
				Name:      "builder",
				PromptRef: ".agents/builder.md",
			},
			Sandbox: SandboxSpec{TemplateRef: "go-dev"},
			Work:    WorkSpec{Instructions: "fix the failing test"},
		},
	}

	t.Run("injects session file and tool rounds", func(t *testing.T) {
		script, err := BuildCIRepairScript(task, "repair instructions", CIRepairOptions{
			SessionFile:   "/tmp/s.json",
			MaxToolRounds: 3,
		})
		if err != nil {
			t.Fatalf("BuildCIRepairScript() error = %v", err)
		}
		if !strings.Contains(script, "export AI_FACTORY_SESSION_FILE=") {
			t.Errorf("script missing AI_FACTORY_SESSION_FILE export:\n%s", script)
		}
		// A repair pass must never write its own session back over the main
		// snapshot (round-over-round accumulation overflows the model input
		// window by the third repair); the read-only marker must be set.
		if !strings.Contains(script, "export AI_FACTORY_SESSION_READONLY=1") {
			t.Errorf("script missing AI_FACTORY_SESSION_READONLY=1 export:\n%s", script)
		}
		if !strings.Contains(script, "export OPENAI_MAX_TOOL_ROUNDS=3") {
			t.Errorf("script missing OPENAI_MAX_TOOL_ROUNDS=3 export:\n%s", script)
		}
		if !strings.HasPrefix(script, "set -eu") {
			t.Errorf("script does not start with set -eu:\n%s", script)
		}
	})

	t.Run("omits env when options are empty", func(t *testing.T) {
		script, err := BuildCIRepairScript(task, "repair instructions", CIRepairOptions{})
		if err != nil {
			t.Fatalf("BuildCIRepairScript() error = %v", err)
		}
		if strings.Contains(script, "AI_FACTORY_SESSION_FILE") {
			t.Errorf("script should not contain AI_FACTORY_SESSION_FILE export:\n%s", script)
		}
		if strings.Contains(script, "OPENAI_MAX_TOOL_ROUNDS") {
			t.Errorf("script should not contain OPENAI_MAX_TOOL_ROUNDS export:\n%s", script)
		}
	})
}
