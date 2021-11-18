package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/mohae/deepcopy"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Pipeline struct {
	Steps []interface{} `yaml:"steps"`
}

func generateProjectSteps(steps []interface{}, step interface{}, projects []Project) []interface{} {
	projectSteps := make([]interface{}, 0)

	for _, project := range projects {

		stepCopy := deepcopy.Copy(step)
		stepCopyMap := stepCopy.(map[interface{}]interface{})

		if project.checkProjectRules(stepCopyMap) {
			// Unique project level label
			stepCopyMap["label"] = fmt.Sprintf("%s %s", stepCopyMap["label"], project.Label)

			// Set default buildpipe environment variables and
			// set project env vars as step env vars
			env := stepCopyMap["env"].(map[interface{}]interface{})
			env["BUILDPIPE_PROJECT_LABEL"] = project.Label
			env["BUILDPIPE_PROJECT_PATH"] = project.getMainPath()
			for envVarName, envVarValue := range project.Env {
				env[envVarName] = envVarValue
			}

			// Unique project level key, if present
			if val, ok := stepCopyMap["key"]; ok {
				stepCopyMap["key"] = fmt.Sprintf("%s:%s", val, project.Label)
			}

			// Populate any yaml level buildpipe env vars we need
			// We are explicitly checking certain keys for now to move mono repo forward

			// Check for step specific github status check
			if notify, ok := stepCopyMap["notify"].([]interface{}); ok {
				for _, element := range notify {
					if commitStatusMap, ok := element.(map[interface{}]interface{}); ok {
						if commitStatus, ok := commitStatusMap["github_commit_status"].(map[interface{}]interface{}); ok {
							if context, ok := commitStatus["context"].(string); ok {
								commitStatus["context"] = stringReplaceProjectLabel(context, project)
							}
						}
					}
				}
			}

			// Check for step specific cache
			if plugins, ok := stepCopyMap["plugins"].([]interface{}); ok {
				fmt.Println("plugins check", plugins)

				for _, element := range plugins {
					if pluginMap, ok := element.(map[interface{}]interface{}); ok {
						cachePluginKey := "ssh://git@github.com/Vkt0r/cache-buildkite-plugin.git#skip_restore_upload"

						if cachePlugin, ok := pluginMap[cachePluginKey].(map[interface{}]interface{}); ok {
							if cacheId, ok := cachePlugin["id"].(string); ok {
								cachePlugin["id"] = stringReplaceProjectLabel(cacheId, project)
							}
						}
					}
				}
			}

			// If the step includes a depends_on clause, we need to validate whether each dependency
			// is a project-scoped step. If so, the dependency has the current project name added
			// to it to match the unique key given above.
			if val, ok := stepCopyMap["depends_on"]; ok {
				// depends_on can be an array or a string
				dependencyList, ok := val.([]interface{})
				if !ok {
					dependencyList = []interface{}{val}
					stepCopyMap["depends_on"] = dependencyList
				}

				for i, dependency := range dependencyList {
					depStr := dependency.(string)

					if step := findStepByKey(steps, depStr); step != nil {
						if isProjectScopeStep(step) {
							dependencyList[i] = fmt.Sprintf("%s:%s", depStr, project.Label)
						}
					}
				}
			}

			projectSteps = append(projectSteps, stepCopy)
		}
	}

	return projectSteps
}

func stringReplaceProjectLabel(input string, project Project) string {
	// handle all cases of env var interpolation
	lowerLabel := strings.ToLower(project.Label)
	envVarEscapedWithBraces := strings.ReplaceAll(input, "$${BUILDPIPE_PROJECT_LABEL}", lowerLabel)
	envVarWithBraces := strings.ReplaceAll(envVarEscapedWithBraces, "${BUILDPIPE_PROJECT_LABEL}", lowerLabel)
	envVarEscaped := strings.ReplaceAll(envVarWithBraces, "$$BUILDPIPE_PROJECT_LABEL", lowerLabel)
	return strings.ReplaceAll(envVarEscaped, "$BUILDPIPE_PROJECT_LABEL", lowerLabel)
}

func isProjectScopeStep(step map[interface{}]interface{}) bool {
	if env, ok := step["env"].(map[interface{}]interface{}); ok {
		if value, ok := env["BUILDPIPE_SCOPE"]; ok {
			return value == "project"
		}
	}
	return false
}

func findStepByKey(steps []interface{}, stepKey string) map[interface{}]interface{} {
	for _, step := range steps {
		// skip wait commands
		stepMap, ok := step.(map[interface{}]interface{})
		if !ok {
			continue
		}
		// grab key if it has one and check whether it is project scoped
		foundStepKey, ok := stepMap["key"]
		if ok && stepKey == foundStepKey {
			return stepMap
		}
	}
	return nil
}

func generatePipeline(steps []interface{}, pipelineEnv map[string]string, projects []Project) *Pipeline {
	generatedSteps := make([]interface{}, 0)

	for _, step := range steps {
		stepMap, ok := step.(map[interface{}]interface{})
		if !ok {
			generatedSteps = append(generatedSteps, step)
			continue
		}

		env, foundEnv := stepMap["env"].(map[interface{}]interface{})
		_, foundBlockStep := stepMap["block"].(string)

		if !foundBlockStep {
			if !foundEnv {
				env = make(map[interface{}]interface{})
				stepMap["env"] = env
			}

			for envVarName, envVarValue := range pipelineEnv {
				env[envVarName] = envVarValue
			}
		}

		value, ok := env["BUILDPIPE_SCOPE"]
		if ok && value == "project" {
			projectSteps := generateProjectSteps(steps, step, projects)
			generatedSteps = append(generatedSteps, projectSteps...)
		} else {
			generatedSteps = append(generatedSteps, step)
		}
	}

	return &Pipeline{
		Steps: generatedSteps,
	}
}

func uploadPipeline(pipeline Pipeline) {
	tmpFile, err := ioutil.TempFile(os.TempDir(), "buildpipe-")
	if err != nil {
		log.Fatalf("Cannot create temporary file: %s\n", err)
	}
	defer os.Remove(tmpFile.Name())

	data, err := yaml.Marshal(&pipeline)

	fmt.Printf("Pipeline:\n%s", string(data))

	err = ioutil.WriteFile(tmpFile.Name(), data, 0644)
	if err != nil {
		log.Fatalf("Error writing outfile: %s\n", err)
	}

	execCommand("buildkite-agent", []string{"pipeline", "upload", tmpFile.Name()})
}
