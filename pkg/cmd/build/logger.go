// Copyright 2023 The Okteto Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/okteto/okteto/pkg/build/buildkit"
	oktetoLog "github.com/okteto/okteto/pkg/log"
	"github.com/okteto/okteto/pkg/types"
	"github.com/tonistiigi/units"
)

var (
	nameRegex = regexp.MustCompile(`--name\s+"([^"]+)"`)
)

const (
	// largeContextThreshold is the threshold (in bytes) by which a context is catalogued as large or not (50MB)
	largeContextThreshold = 50000000
)

func deployDisplayer(ctx context.Context, ch chan *client.SolveStatus, o *types.BuildOptions) error {
	// TODO: import build timeout
	timeout := time.NewTicker(10 * time.Minute)
	defer timeout.Stop()

	oktetoLog.Spinner("Synchronizing context...")
	oktetoLog.StartSpinner()
	defer oktetoLog.StopSpinner()

	t := newTrace()

	var done bool
	var outputMode string

	switch o.OutputMode {
	case DeployOutputModeOnBuild:
		outputMode = DeployOutputModeOnBuild
	case DestroyOutputModeOnBuild:
		outputMode = DestroyOutputModeOnBuild
	case TestOutputModeOnBuild:
		outputMode = TestOutputModeOnBuild
	default:
		outputMode = DeployOutputModeOnBuild
	}

	for {
		select {
		case <-ctx.Done():
			oktetoLog.StopSpinner()
			return ctx.Err()
		case <-timeout.C:
		case ss, ok := <-ch:
			if ok {
				if err := t.update(ss); err != nil {
					oktetoLog.Info(err.Error())
					continue
				}
				t.display(outputMode)
				t.removeCompletedSteps()
			} else {
				done = true
			}
			if done {
				oktetoLog.StopSpinner()
				if t.err != nil {
					return t.err
				}
				return nil
			}
		}
	}
}

type trace struct {
	err           error
	ongoing       map[string]*vertexInfo
	stages        map[string]bool
	showCtxAdvice bool
}

func newTrace() *trace {
	return &trace{
		ongoing:       map[string]*vertexInfo{},
		stages:        map[string]bool{},
		showCtxAdvice: true,
	}
}

func (t *trace) update(ss *client.SolveStatus) error {
	for _, rawVertex := range ss.Vertexes {
		v, ok := t.ongoing[rawVertex.Digest.Encoded()]
		if !ok {
			v = &vertexInfo{
				name:   rawVertex.Name,
				cached: rawVertex.Cached,
			}
			t.ongoing[rawVertex.Digest.Encoded()] = v
		}
		if rawVertex.Error != "" {
			return fmt.Errorf("error on stage %s: %s", rawVertex.Name, rawVertex.Error)
		}
		if rawVertex.Cached {
			v.cached = true
		}
		if rawVertex.Completed != nil {
			v.completed = true
			continue
		}
	}
	for _, s := range ss.Statuses {
		v, ok := t.ongoing[s.Vertex.Encoded()]
		if !ok {
			continue // shouldn't happen
		}
		v.completed = s.Completed != nil
		v.currentTransferedContext = s.Current
		v.totalTransferedContext = s.Total
	}
	for _, l := range ss.Logs {
		v, ok := t.ongoing[l.Vertex.Encoded()]
		if !ok {
			continue // shouldn't happen
		}
		newLogs := strings.Split(string(l.Data), "\n")
		v.logs = append(v.logs, newLogs...)
	}
	return nil
}

func (t *trace) display(progress string) {
	for _, v := range t.ongoing {
		if isTransferringContext(v.name) {
			if v.currentTransferedContext != 0 {
				currentLoadedCtx := units.Bytes(v.currentTransferedContext)
				if t.showCtxAdvice && currentLoadedCtx > largeContextThreshold {
					t.showCtxAdvice = false
					oktetoLog.Information("You can use '.oktetoignore' file to optimize the context used to deploy your development environment.")
				}
				oktetoLog.Spinner(fmt.Sprintf("Synchronizing context: %.2f", currentLoadedCtx))
			}
		}
		if hasCommandLogs(v) {
			switch progress {
			case DeployOutputModeOnBuild:
				oktetoLog.Spinner("Deploying your development environment...")
			case DestroyOutputModeOnBuild:
				oktetoLog.Spinner("Destroying your development environment...")
			case TestOutputModeOnBuild:
				oktetoLog.Spinner("Running tests...")
			}

			for _, log := range v.logs {
				if log == "" {
					continue
				}

				var text oktetoLog.JSONLogFormat
				if err := json.Unmarshal([]byte(log), &text); err != nil {
					oktetoLog.Debugf("could not parse %s: %s", log, err)
					continue
				}
				if text.Stage == "" {
					oktetoLog.Infof("received log without stage: %s", text.Message)
					continue
				}
				oktetoLog.SetStage(text.Stage)
				switch text.Stage {
				case "done":
					continue
				case "Load manifest":
					if text.Level == "error" {
						oktetoLog.Fail("%s", text.Message)
					}
				default:
					// Print the information message about the stage if needed
					if _, ok := t.stages[text.Stage]; !ok {
						if progress != TestOutputModeOnBuild {
							oktetoLog.Information("Running stage '%s'", text.Stage)
						}
						t.stages[text.Stage] = true
					}
					if text.Level == "error" {
						if text.Stage != "" {
							t.err = buildkit.CommandErr{
								Stage:  text.Stage,
								Err:    fmt.Errorf("%s", text.Message),
								Output: progress,
							}
						}
					} else {
						oktetoLog.Println(text.Message)
					}

				}
			}
			v.logs = []string{}
			oktetoLog.SetStage("")
		}
	}
}

func (t *trace) removeCompletedSteps() {
	for k, v := range t.ongoing {
		if v.completed {
			// We need to specify the command test in order to not affect okteto deploy on remote
			if strings.Contains(v.name, "remote-run test") && v.cached {
				match := nameRegex.FindStringSubmatch(v.name)
				if len(match) > 1 {
					name := match[1]
					oktetoLog.Information("Skipping test container '%s', CACHED", name)
				} else {
					oktetoLog.Information("Skipping test container, CACHED")
				}
			}
			delete(t.ongoing, k)
		}
	}
}

func hasCommandLogs(v *vertexInfo) bool {
	return len(v.logs) != 0
}

func isTransferringContext(name string) bool {
	isInternal := strings.HasPrefix(name, "[internal]")
	isLoadingCtx := strings.Contains(name, "load build")
	return isInternal && isLoadingCtx
}

type vertexInfo struct {
	name                     string
	logs                     []string
	currentTransferedContext int64
	totalTransferedContext   int64
	completed                bool
	cached                   bool
}
