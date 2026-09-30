package formats

import (
	"reflect"
	"testing"
)

func TestTaskArgs(t *testing.T) {
	task := Task{Script: `echo "$TASK" "$TASK_NAME" "${TASK_COUNT:-1}"
curl -H "Authorization: Bearer $TASK_TOKEN" "$TASK_URL"
Write-Output $env:TASK_PATH $Env:TASK_NAME
echo "$TASKLESS $NOT_TASK_X"`}
	want := []string{"count", "name", "path", "url"}
	if got := task.Args(); !reflect.DeepEqual(got, want) {
		t.Errorf("Args() = %v, want %v", got, want)
	}
	if got := (Task{Script: "echo hi"}).Args(); got != nil {
		t.Errorf("Args() of a script with no TASK_ vars = %v, want none", got)
	}
}
