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
	"errors"
	"fmt"
	"strings"
)

// CloneURL returns the explicit clone URL or builds a provider default.
func (s SourceSpec) CloneURLOrDefault() (string, error) {
	if s.CloneURL != "" {
		return s.CloneURL, nil
	}

	host := s.Host
	switch s.Provider {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderGitLab:
		if host == "" {
			host = "gitlab.com"
		}
	default:
		return "", fmt.Errorf("unsupported git provider %q", s.Provider)
	}

	repository := strings.TrimPrefix(s.Repository, "/")
	return fmt.Sprintf("https://%s/%s.git", host, repository), nil
}

// ForkCloneURL derives the clone URL of a same-named fork given the fork owner.
func (s SourceSpec) ForkCloneURL(forkOwner string) (string, error) {
	if strings.TrimSpace(forkOwner) == "" {
		return "", errors.New("forkOwner is required to derive fork clone URL")
	}
	repo := strings.TrimPrefix(s.Repository, "/")
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("repository must look like owner/repo, got %q", s.Repository)
	}
	host := s.Host
	switch s.Provider {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderGitLab:
		if host == "" {
			host = "gitlab.com"
		}
	default:
		return "", fmt.Errorf("unsupported git provider %q", s.Provider)
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, forkOwner, parts[1]), nil
}
