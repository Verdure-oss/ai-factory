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

import "testing"

// TestParseCIEventExtractsHeadSHA proves both CI event shapes yield the head
// sha the verdict belongs to: check_suite events carry it at the top-level
// check_suite.head_sha, check_run events inside check_run.head_sha. This is the
// payload GitHub pushes the instant a check completes — the value the repair
// rounds bind their evaluation to, immune to PR-head reflection lag.
func TestParseCIEventExtractsHeadSHA(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantOwner string
		wantRepo  string
		wantBr    string
		wantSHA   string
		wantErr   bool
	}{
		{
			name: "check_suite completed with suite head_sha",
			body: `{"action":"completed","check_suite":{"head_branch":"factory-task/abc","head_sha":"deadbeef1234"},"repository":{"full_name":"owner/repo"}}`,
			wantOwner: "owner", wantRepo: "repo", wantBr: "factory-task/abc", wantSHA: "deadbeef1234",
		},
		{
			name: "check_run completed with run head_sha",
			body: `{"action":"completed","check_run":{"head_sha":"cafebabe99"},"repository":{"full_name":"owner/repo"}}`,
			wantOwner: "owner", wantRepo: "repo", wantSHA: "cafebabe99",
		},
		{
			name: "queued action ignored",
			body: `{"action":"queued","check_suite":{"head_branch":"b","head_sha":"aaaa"},"repository":{"full_name":"owner/repo"}}`,
			wantErr: true,
		},
		{
			name: "check_run head_sha wins over empty suite head_sha",
			body: `{"action":"completed","check_suite":{"head_branch":"b","head_sha":""},"check_run":{"head_sha":"runsha"},"repository":{"full_name":"owner/repo"}}`,
			wantOwner: "owner", wantRepo: "repo", wantBr: "b", wantSHA: "runsha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, branch, sha, err := parseCIEvent([]byte(tt.body), true)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCIEvent = (%q,%q,%q,%q,nil), want error", owner, repo, branch, sha)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCIEvent: %v", err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo || branch != tt.wantBr || sha != tt.wantSHA {
				t.Errorf("parseCIEvent = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
					owner, repo, branch, sha, tt.wantOwner, tt.wantRepo, tt.wantBr, tt.wantSHA)
			}
		})
	}
}