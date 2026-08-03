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
	"os"
	"strings"
)

const (
	// secretDir is the mount path for K8s Secret volume.
	// Each key in the Secret becomes a file in this directory.
	// K8s automatically syncs Secret updates to mounted files (~30s).
	secretDir = "/etc/ai-factory/secret"

	// configDir is the mount path for K8s ConfigMap volume.
	// Each key in the ConfigMap becomes a file in this directory.
	configDir = "/etc/ai-factory/config"
)

// ReadConfig reads a configuration value by name, checking in order:
//  1. Secret file: /etc/ai-factory/secret/<name>
//  2. ConfigMap file: /etc/ai-factory/config/<name>
//  3. Environment variable: os.Getenv(name)
//
// This enables hot-reload of credentials and config in K8s deployments
// (Secret/ConfigMap volume mounts are synced automatically by kubelet),
// while falling back to env vars for local development.
func ReadConfig(name string) string {
	// Try secret file first (higher priority for sensitive values)
	if v := readFile(secretDir, name); v != "" {
		return v
	}
	// Try configmap file
	if v := readFile(configDir, name); v != "" {
		return v
	}
	// Fall back to environment variable
	return os.Getenv(name)
}

// readFile reads a single value from a mounted config directory.
// Returns empty string if the file doesn't exist or can't be read.
func readFile(dir, name string) string {
	data, err := os.ReadFile(dir + "/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
