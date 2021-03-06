package job

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
	"github.com/tychoish/grip"
)

// ShellJob is an amboy.Job implementation that runs shell commands in
// the context of an amboy.Job object.
type ShellJob struct {
	Command    string            `bson:"command" json:"command" yaml:"command"`
	Output     string            `bson:"output" json:"output" yaml:"output"`
	WorkingDir string            `bson:"working_dir" json:"working_dir" yaml:"working_dir"`
	Env        map[string]string `bson:"env" json:"env" yaml:"env"`
	args       []string

	*Base `bson:"job_base" json:"job_base" yaml:"job_base"`
	sync.RWMutex
}

// NewShellJob takes the command, as a string along with the name of a
// file that the command would create, and returns a pointer to a
// ShellJob object. If the "creates" argument is an empty string then
// the command always runs, otherwise only if the file specified does
// not exist. You can change the dependency with the SetDependency
// argument.
func NewShellJob(cmd string, creates string) *ShellJob {
	j := NewShellJobInstance()
	j.Command = cmd
	j.getArgsFromCommand()

	if creates != "" {
		j.SetDependency(dependency.NewCreatesFile(creates))
	}

	if len(j.args) == 0 {
		j.SetID(fmt.Sprintf("%d.shell-job", GetNumber()))
	} else {
		j.SetID(fmt.Sprintf("%s-%d.shell-job", j.args[0], GetNumber()))
	}

	return j
}

// NewShellJobInstance returns a pointer to an initialized ShellJob
// instance, but does not set the command or the name. Use when the
// command is not known at creation time.
func NewShellJobInstance() *ShellJob {
	j := &ShellJob{
		Env: make(map[string]string),
		Base: &Base{
			JobType: amboy.JobType{
				Name:    "shell",
				Version: 1,
				Format:  amboy.BSON,
			},
		},
	}
	j.SetDependency(dependency.NewAlways())
	return j
}

func (j *ShellJob) getArgsFromCommand() {
	j.Lock()
	defer j.Unlock()
	j.args = strings.Split(j.Command, " ")
}

// Run executes the shell commands. Add keys to the Env map to modify
// the environment, or change the value of the WorkingDir property to
// set the working directory for this command. Captures output into
// the Output attribute, and returns the error value of the command.
func (j *ShellJob) Run() {
	defer j.MarkComplete()
	grip.Debugf("running %s", j.Command)

	j.getArgsFromCommand()

	j.RLock()
	cmd := exec.Command(j.args[0], j.args[1:]...)
	j.RUnlock()

	cmd.Dir = j.WorkingDir
	cmd.Env = j.getEnVars()

	output, err := cmd.CombinedOutput()
	j.AddError(err)

	j.Lock()
	defer j.Unlock()

	j.Output = strings.TrimSpace(string(output))
	j.IsComplete = true
}

func (j *ShellJob) getEnVars() []string {
	if len(j.Env) == 0 {
		return []string{}
	}

	output := make([]string, len(j.Env))

	for k, v := range j.Env {
		output = append(output, strings.Join([]string{k, v}, "="))
	}

	return output
}
