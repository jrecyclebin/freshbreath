package formats

import (
	"regexp"
	"slices"
	"strings"
)

// Task represents a single named script parsed from a tasks file.
type Task struct {
	Name   string `json:"name"`
	Desc   string `json:"description"`
	Script string `json:"-"` // never serialized
}

// parseHeader checks whether a line is a valid [name] header for either a
// task or a virtual tool. Returns (name, desc, true) on success, or
// ("", "", false) otherwise. A valid header must start with '[', contain a
// ']' on the same line, and have a non-empty name between them.
func parseHeader(line string) (name, desc string, ok bool) {
	if !strings.HasPrefix(line, "[") {
		return "", "", false
	}
	closeIdx := strings.Index(line, "]")
	if closeIdx < 1 { // no ']' or empty name like "[]"
		return "", "", false
	}
	name = line[1:closeIdx]
	desc = strings.TrimSpace(line[closeIdx+1:])
	return name, desc, true
}

// ParseTasksFile parses the task definitions from a tasks file.
//
// Format: a [task-name] header optionally followed by a description on the
// same line, then the script body until the next header, a "---" separator,
// or EOF. The "---" delimiter matches the virtual tool file format and lets
// callers visually separate tasks without consuming the marker as script body.
//
//	[greet] Say hello to someone
//	echo "Hello, $TASK_NAME"
//	---
//	[build] Compile the project
//	make all
//
// Lines starting with '[' that don't parse as a valid header are treated as
// script body, so bash expressions like ${arr[0]} are safe.
func ParseTasksFile(data []byte) []Task {
	var tasks []Task
	var cur *Task
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "---" {
			if cur != nil {
				tasks = append(tasks, *cur)
				cur = nil
			}
			continue
		}
		if name, desc, ok := parseHeader(line); ok {
			if cur != nil {
				tasks = append(tasks, *cur)
			}
			cur = &Task{Name: name, Desc: desc}
		} else if cur != nil {
			if cur.Script != "" {
				cur.Script += "\n"
			}
			cur.Script += line
		}
	}
	if cur != nil {
		tasks = append(tasks, *cur)
	}
	return tasks
}

// taskArgRef matches a script's reference to a TASK_<NAME> environment
// variable: $TASK_X and ${TASK_X} in sh, $env:TASK_X in PowerShell.
var taskArgRef = regexp.MustCompile(`\$(?:\{|(?i:env):)?TASK_([A-Za-z0-9_]+)`)

// Args lists the arguments a task's script reads, lowercased and sorted:
// each $TASK_<NAME> it references, less TASK_TOKEN, which the server
// supplies. Scripts don't declare their arguments, so this is the only
// signature an MCP client gets to see.
func (t Task) Args() []string {
	var args []string
	for _, m := range taskArgRef.FindAllStringSubmatch(t.Script, -1) {
		name := strings.ToLower(m[1])
		if name != "token" && !slices.Contains(args, name) {
			args = append(args, name)
		}
	}
	slices.Sort(args)
	return args
}
