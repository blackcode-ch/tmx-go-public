package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestCodexSafeDefaultArgs(t *testing.T) {
	withGOOS(t, "linux") // Windows: TestCodexWindowsSandboxOffByDefault
	for _, mode := range []string{"", "auto", "bypassPermissions", "yolo", "full-access"} {
		t.Run(mode, func(t *testing.T) {
			spec := runner.Spec{PermissionMode: mode}
			for _, args := range [][]string{CodexArgs(spec, ""), CodexAppServerArgs(spec)} {
				joined := strings.Join(args, " ")
				for _, forbidden := range []string{"danger-full-access", "dangerously", "yolo", "hide_full_access_warning"} {
					if strings.Contains(joined, forbidden) {
						t.Fatalf("unsafe default: %v", args)
					}
				}
				for _, required := range []string{"workspace-write", "on-request", "sandbox_workspace_write.writable_roots=[]", "sandbox_workspace_write.exclude_tmpdir_env_var=true", "sandbox_workspace_write.exclude_slash_tmp=true"} {
					if !strings.Contains(joined, required) {
						t.Fatalf("missing %q: %v", required, args)
					}
				}
			}
		})
	}
}

func TestCodexExplicitFullAccessArgs(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			withGOOS(t, goos)
			for _, windowsSandbox := range []bool{false, true} {
				spec := runner.Spec{CodexFullAccess: true, CodexWindowsSandbox: windowsSandbox}
				if !slices.Contains(CodexArgs(spec, ""), "--dangerously-bypass-approvals-and-sandbox") {
					t.Fatal("explicit opt-in missing")
				}
				if !slices.Contains(CodexAppServerArgs(spec), `sandbox_mode="danger-full-access"`) {
					t.Fatal("app-server opt-in missing")
				}
				spec.PermissionMode = "plan"
				wantSandbox, wantApproval := "read-only", "on-request"
				// Windows intentionally disables the sandbox unless opted in;
				// plan mode must still override full access with untrusted approval.
				if goos == "windows" && !windowsSandbox {
					wantSandbox, wantApproval = "danger-full-access", "untrusted"
				}
				if sandbox, approval := CodexPolicy(spec); sandbox != wantSandbox || approval != wantApproval {
					t.Fatalf("plan (windows sandbox=%v): %s %s; want %s %s", windowsSandbox, sandbox, approval, wantSandbox, wantApproval)
				}
			}
		})
	}
}

func TestCodexTerminalResumeKeepsSandbox(t *testing.T) {
	withGOOS(t, "linux")
	for _, session := range []string{"", "existing-thread"} {
		command, err := TerminalCommand(runner.Spec{Harness: model.HarnessCodex, Command: "/fake/codex", SessionID: session, PermissionMode: "bypassPermissions"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(command.Args, "workspace-write") || slices.Contains(command.Args, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("unsafe terminal launch: %v", command.Args)
		}
	}
}

// withCodexHomeDirs overrides codexHomeDirs for the duration of the test.
func withCodexHomeDirs(t *testing.T, dirs ...string) {
	t.Helper()
	prev := codexHomeDirs
	codexHomeDirs = func() []string { return dirs }
	t.Cleanup(func() { codexHomeDirs = prev })
}

// withCodexExtraRoots overrides codexExtraRoots for the duration of the test.
func withCodexExtraRoots(t *testing.T, dirs ...string) {
	t.Helper()
	prev := codexExtraRoots
	codexExtraRoots = func() []string { return dirs }
	t.Cleanup(func() { codexExtraRoots = prev })
}

// mklinkJunction creates a Windows junction at link pointing to target, using mklink (no
// symlink privilege required). Skips on non-Windows. target must be a test-owned temp
// directory, never the real home or a real drive root: a junction followed by a careless
// RemoveAll would delete through it.
func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows-only")
	}
	cmd := hiddenexec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v: %s", link, target, err, out)
	}
	// Cleanup removes only the link itself, never RemoveAll on the link or its parent — that
	// would follow the junction and delete the target.
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("remove junction %s: %v", link, err)
		}
	})
}

func TestCodexRefusesHomeRoot(t *testing.T) {
	home := t.TempDir()
	withCodexHomeDirs(t, home)

	for _, full := range []bool{false, true} {
		if err := validateCodexSpec(runner.Spec{Cwd: home, CodexFullAccess: full}, false); err == nil {
			t.Fatalf("accepted injected home root as cwd (full=%v)", full)
		}
	}

	sub := filepath.Join(home, "project")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateCodexSpec(runner.Spec{Cwd: sub}, false); err != nil {
		t.Fatalf("rejected subfolder of home: %v", err)
	}
}

func TestCodexRefusesJunctionToHomeRoot(t *testing.T) {
	home := t.TempDir()
	withCodexHomeDirs(t, home)

	parent := t.TempDir()
	link := filepath.Join(parent, "home-link")
	mklinkJunction(t, link, home)

	if err := validateCodexSpec(runner.Spec{Cwd: link}, false); err == nil {
		t.Fatal("accepted junction to injected home root")
	}
}

func TestCodexRefusesJunctionToDriveRoot(t *testing.T) {
	fakeDriveRoot := t.TempDir()
	withCodexExtraRoots(t, fakeDriveRoot)

	parent := t.TempDir()
	link := filepath.Join(parent, "drive-link")
	mklinkJunction(t, link, fakeDriveRoot)

	if err := validateCodexSpec(runner.Spec{Cwd: link}, false); err == nil {
		t.Fatal("accepted junction to injected fake drive root")
	}
}

// TestCodexRefusesNestedJunctionToHomeRoot is the reviewer's NO-GO repro: a junction to a
// junction. actual/home is the injected home; alias is a junction to actual; outer is a
// junction to alias/home. The hand-written resolveCodexLinks walk only followed the last path
// component's reparse point, so Validate(Cwd: outer) was wrongly accepted even though
// os.Stat/the OS itself follows outer all the way through to home.
func TestCodexRefusesNestedJunctionToHomeRoot(t *testing.T) {
	actual := t.TempDir()
	home := filepath.Join(actual, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	withCodexHomeDirs(t, home)

	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	mklinkJunction(t, alias, actual)

	outerParent := t.TempDir()
	outer := filepath.Join(outerParent, "outer")
	mklinkJunction(t, outer, filepath.Join(alias, "home"))

	if err := validateCodexSpec(runner.Spec{Cwd: outer}, false); err == nil {
		t.Fatal("accepted nested junction resolving to injected home root")
	}
}

func TestCodexAllowsSubfolderThroughNestedJunction(t *testing.T) {
	actual := t.TempDir()
	home := filepath.Join(actual, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	withCodexHomeDirs(t, home)

	project := filepath.Join(home, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}

	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	mklinkJunction(t, alias, actual)

	outerParent := t.TempDir()
	outer := filepath.Join(outerParent, "outer")
	mklinkJunction(t, outer, filepath.Join(alias, "home", "project"))

	if err := validateCodexSpec(runner.Spec{Cwd: outer}, false); err != nil {
		t.Fatalf("rejected subfolder of home reached through nested junction: %v", err)
	}
}

func TestCodexRefusesSymlinkToRoot(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "root-link")
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateCodexSpec(runner.Spec{Cwd: link}, false); err == nil {
		t.Fatal("accepted symlink to root")
	}
}

func TestCodexRefusesUnsafeCwd(t *testing.T) {
	for _, cwd := range []string{"", "/", `C:\`, `D:/`, `C:`, `C:relative`, `C:\Windows`, `c:\WINDOWS\System32`, `C:\work\..\Windows`, `C:\Program Files\tool`, `C:\ProgramData\tool`, `\\server\share`, `\\?\C:\work`, "/etc", "/usr/local", "/opt/tool"} {
		t.Run(cwd, func(t *testing.T) {
			if !unsafeCodexPath(cwd) {
				t.Fatalf("unsafe path not recognized: %q", cwd)
			}
			for _, full := range []bool{false, true} {
				if err := validateCodexSpec(runner.Spec{Cwd: cwd, CodexFullAccess: full}, false); err == nil {
					t.Fatalf("accepted unsafe cwd %q (full=%v)", cwd, full)
				}
			}
		})
	}
	dir := t.TempDir()
	if err := validateCodexSpec(runner.Spec{Cwd: dir}, false); err != nil {
		t.Fatal(err)
	}
	if err := validateCodexSpec(runner.Spec{Cwd: dir}, true); err == nil {
		t.Fatal("accepted elevated launch")
	}
	for _, args := range [][]string{{"--yolo"}, {"--sandbox", "danger-full-access"}, {"-c", "sandbox_workspace_write.writable_roots=['/']"}, {"--cd", "/"}} {
		if err := validateCodexSpec(runner.Spec{Cwd: dir, ExtraArgs: args}, false); err == nil {
			t.Fatalf("accepted extra args: %v", args)
		}
	}
	if err := validateCodexSpec(runner.Spec{Cwd: dir, AddDirs: []string{"/"}}, false); err == nil {
		t.Fatal("accepted additional writable roots")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateCodexSpec(runner.Spec{Cwd: file}, false); err == nil {
		t.Fatal("accepted file as cwd")
	}
}

// macOS keeps each user's temp and cache folders under /var/folders (real path /private/var/folders):
// a working folder deep inside one is the user's; everything else in /var and /private/var is not.
func TestCodexMacUserTempAndPrivateVar(t *testing.T) {
	for cwd, unsafe := range map[string]bool{
		"/var/folders/vt/d6n2f_1s/T/TestX/001":         false,
		"/private/var/folders/vt/d6n2f_1s/T/TestX/001": false,
		"/var/folders/vt/d6n2f_1s/T":                   false,
		"/var/folders":                                 true,
		"/var/folders/vt":                              true,
		"/var/folders/vt/d6n2f_1s":                     true,
		"/var/log":                                     true,
		"/private/var/log":                             true,
		"/private/var/root":                            true,
		"/private/var/db/x":                            true,
		"/private/var":                                 true,
		"/Users/someone/Projects/app":                  false,
	} {
		if got := unsafeCodexPath(cwd); got != unsafe {
			t.Errorf("%s: unsafe %v, want %v", cwd, got, unsafe)
		}
	}
}

// The full-access confirmation is hidden only for an agent the owner opted into full access
// (it already has it); everyone else keeps it, and no gate answers it (detect_test.go).
func TestCodexFullAccessWarningHiddenOnlyWithOptIn(t *testing.T) {
	if !strings.Contains(strings.Join(CodexArgs(runner.Spec{CodexFullAccess: true}, ""), " "), "notice.hide_full_access_warning=true") {
		t.Fatal("full-access launch shows the confirmation it already answered")
	}
	for _, spec := range []runner.Spec{{}, {PermissionMode: "plan", CodexFullAccess: true}, {PermissionMode: "bypassPermissions"}} {
		if strings.Contains(strings.Join(CodexArgs(spec, ""), " "), "hide_full_access_warning") {
			t.Fatalf("confirmation hidden for %+v", spec)
		}
	}
}

// withGOOS runs the policy as on another platform.
func withGOOS(t *testing.T, goos string) {
	t.Helper()
	prev := codexGOOS
	codexGOOS = goos
	t.Cleanup(func() { codexGOOS = prev })
}

// On Windows Codex's own sandbox is off unless the owner opted in (9 Sep: it rewrites file
// permissions): unsandboxed and asking, as before #20. Full access stays the owner's opt-in.
func TestCodexWindowsSandboxOffByDefault(t *testing.T) {
	withGOOS(t, "windows")
	for _, c := range []struct {
		spec              runner.Spec
		sandbox, approval string
	}{
		{runner.Spec{}, "danger-full-access", "on-request"},
		{runner.Spec{PermissionMode: "bypassPermissions"}, "danger-full-access", "on-request"},
		{runner.Spec{PermissionMode: "plan"}, "danger-full-access", "untrusted"},
		{runner.Spec{PermissionMode: "plan", CodexFullAccess: true}, "danger-full-access", "untrusted"},
		{runner.Spec{CodexFullAccess: true}, "", "never"},
		// The owner's opt-in: the sandbox as on other platforms, unelevated.
		{runner.Spec{CodexWindowsSandbox: true}, "workspace-write", "on-request"},
		{runner.Spec{CodexWindowsSandbox: true, PermissionMode: "plan"}, "read-only", "on-request"},
		{runner.Spec{CodexWindowsSandbox: true, CodexFullAccess: true}, "", "never"},
	} {
		if sb, ap := CodexPolicy(c.spec); sb != c.sandbox || ap != c.approval {
			t.Errorf("%+v: %q %q, want %q %q", c.spec, sb, ap, c.sandbox, c.approval)
		}
	}
	for _, spec := range []runner.Spec{{}, {PermissionMode: "plan"}} {
		for _, args := range [][]string{CodexArgs(spec, ""), CodexAppServerArgs(spec)} {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "windows.sandbox") || strings.Contains(joined, "workspace-write") || strings.Contains(joined, "read-only") {
				t.Fatalf("Codex's Windows sandbox without the owner's opt-in: %v", args)
			}
			if strings.Contains(joined, "dangerously") {
				t.Fatalf("bypass without the owner's opt-in: %v", args)
			}
		}
	}
	on := runner.Spec{CodexWindowsSandbox: true}
	for _, args := range [][]string{CodexArgs(on, ""), CodexAppServerArgs(on)} {
		if !strings.Contains(strings.Join(args, " "), `windows.sandbox="unelevated"`) {
			t.Fatalf("opted in without the unelevated sandbox: %v", args)
		}
	}
}

// macOS and Linux keep #20's defaults, opt-in or not: their sandboxes change no permissions.
func TestCodexUnixSandboxUnchanged(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		withGOOS(t, goos)
		for _, optIn := range []bool{false, true} {
			if sb, ap := CodexPolicy(runner.Spec{CodexWindowsSandbox: optIn}); sb != "workspace-write" || ap != "on-request" {
				t.Fatalf("%s: %q %q", goos, sb, ap)
			}
			if sb, _ := CodexPolicy(runner.Spec{PermissionMode: "plan", CodexWindowsSandbox: optIn}); sb != "read-only" {
				t.Fatalf("%s plan: %q", goos, sb)
			}
			if strings.Contains(strings.Join(CodexSandboxArgs(runner.Spec{CodexWindowsSandbox: optIn}), " "), "windows.sandbox") {
				t.Fatalf("%s: windows.sandbox passed", goos)
			}
		}
	}
}
