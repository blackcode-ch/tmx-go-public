package guard

import "testing"

func TestCheckExecLiteralArgumentsAndInterpreters(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		outcome Outcome
	}{
		{"go", []string{"test", "./..."}, Allow},
		{"echo", []string{"sudo true; pkill node"}, Allow},
		{"sudo", []string{"true"}, Hold},
		{"pkill", []string{"node"}, Block},
		{"sh", []string{"-c", "sudo true"}, Hold},
		{"sh", []string{"-c", "pkill node"}, Block},
		{"pwsh", []string{"-Command", "Stop-Process -Name node"}, Block},
		{"cmd", []string{"/c", "taskkill /IM node.exe"}, Block},
		{"chmod", []string{"-R", "777", "/"}, Block},
	} {
		for _, platform := range []string{"windows", "linux"} {
			v := CheckExec(Context{OS: platform, Cwd: "/work/project", Own: "/work/project"}, tc.command, tc.args)
			if v.Outcome != tc.outcome {
				t.Errorf("%s %s %v: %+v", platform, tc.command, tc.args, v)
			}
		}
	}
}
