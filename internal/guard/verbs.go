package guard

import (
	"encoding/base64"
	"regexp"
	"strings"
	"unicode/utf16"
)

type handler func(a *analyzer, c *call)

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		// deletes
		"rm": hDelete, "unlink": hDelete, "rmdir": hDelete, "rd": hDelete, "del": hDelete, "erase": hDelete,
		"remove-item": hDelete, "trash": hDelete, "srm": hDelete, "shred": hDelete,
		// moves, copies, writes
		"mv": hMove, "move": hMove, "move-item": hMove, "ren": hMove, "rename": hMove, "rename-item": hMove,
		"cp": hCopy, "copy": hCopy, "copy-item": hCopy, "xcopy": hCopy, "robocopy": hCopy, "rsync": hCopy,
		"ditto": hCopy, "install": hCopy, "ln": hCopy, "scp": hCopy,
		"touch": hWriteArgs, "mkdir": hWriteArgs, "md": hWriteArgs, "new-item": hWriteArgs, "tee": hWriteArgs,
		"tee-object": hWriteArgs, "set-content": hWriteArgs, "add-content": hWriteArgs, "out-file": hWriteArgs,
		"clear-content": hWriteArgs, "truncate": hWriteArgs,
		"dd": hDD, "tar": hArchive, "zip": hArchive, "7z": hArchive, "compress-archive": hArchive,
		// permissions
		"chmod": hPerms, "chown": hPerms, "chgrp": hPerms, "chflags": hPerms, "chattr": hPerms, "setfacl": hPerms,
		"icacls": hACL, "cacls": hACL, "xcacls": hACL, "takeown": hACL, "set-acl": hACL, "subinacl": hACL, "setacl": hACL,
		// disks
		"diskutil": hDiskutil, "wipefs": hDiskAlways, "mke2fs": hDiskAlways, "mkswap": hDiskAlways,
		"mkdosfs": hDiskAlways, "mkntfs": hDiskAlways, "fdisk": hPartition, "sfdisk": hPartition,
		"gdisk": hPartition, "sgdisk": hPartition, "cfdisk": hPartition, "parted": hPartition,
		"format": hFormat, "format-volume": hDiskAlways, "clear-disk": hDiskAlways, "initialize-disk": hDiskAlways,
		"remove-partition": hDiskAlways, "diskpart": hDiskAlways, "cipher": hCipher, "vssadmin": hVSS,
		"wbadmin": hVSS, "wmic": hWMIC, "asr": hASR,
		// boot
		"csrutil": hCsrutil, "bcdedit": hBcdedit, "bcdboot": hBootAlways, "bless": hBootAlways,
		"nvram": hNvram, "reagentc": hReagentc, "mountvol": hMountvol, "efibootmgr": hEfiboot,
		"grub-install": hBootAlways, "kextunload": hBootAlways, "spctl": hSpctl,
		// firewall and Tailscale (machine protection: who reaches this machine)
		"netsh": hNetsh, "ufw": hUfw, "pfctl": hNetAlways, "iptables": hNetAlways, "ip6tables": hNetAlways,
		"nft": hNetAlways, "firewall-cmd": hNetAlways, "tailscale": hTailscale,
		"new-netfirewallrule": hNetAlways, "set-netfirewallrule": hNetAlways, "remove-netfirewallrule": hNetAlways,
		"enable-netfirewallrule": hNetAlways, "disable-netfirewallrule": hNetAlways, "set-netfirewallprofile": hNetAlways,
		"copy-netfirewallrule": hNetAlways, "rename-netfirewallrule": hNetAlways,
		"stop-service": hTailscaleService, "set-service": hTailscaleService, "suspend-service": hTailscaleService,
		"sc.exe": hTailscaleService, "sc": hTailscaleService, "net": hTailscaleService, "net.exe": hTailscaleService,
		// Windows Hello and security keys (the 7 Oct lockout class)
		"ssh-keygen": hSSHKeygen, "ssh-keygen.exe": hSSHKeygen, "fido2-token": hHelloAlways, "fido2-cred": hHelloAlways,
		"fido2-assert": hHelloAlways, "ykman": hHelloAlways, "webauthn": hHelloAlways, "webauthn-cli": hHelloAlways,
		// processes and power
		"pkill": hPkill, "killall": hPkill, "taskkill": hTaskkill, "kill": hKill, "stop-process": hStopProcess,
		"xargs": hXargs, "remove-ciminstance": hCimKill, "invoke-cimmethod": hCimKill,
		"shutdown": hShutdown, "reboot": hPowerAlways, "halt": hPowerAlways, "poweroff": hPowerAlways,
		"stop-computer": hPowerAlways, "restart-computer": hPowerAlways, "init": hInit, "telinit": hInit,
		"systemctl": hSystemctl, "launchctl": hLaunchctl, "loginctl": hLoginctl, "osascript": hOsascript,
		// shells and evaluation
		"sh": hShell, "bash": hShell, "zsh": hShell, "dash": hShell, "ksh": hShell, "mksh": hShell,
		"fish": hShell, "csh": hShell, "tcsh": hShell, "ash": hShell, "python": hInterp, "python3": hInterp,
		"node": hInterp, "perl": hInterp, "ruby": hInterp, "php": hInterp, "deno": hInterp, "bun": hInterp,
		"eval": hEval, "source": hSource, ".": hSource, "invoke-expression": hIEX,
		"cmd": hCmd, "powershell": hPowerShell, "pwsh": hPowerShell,
		"find": hFind, "set-location": hCd, "cd": hCd, "chdir": hCd, "pushd": hCd, "push-location": hCd,
		"popd": hPopd, "pop-location": hPopd,
		// downloads and listings that feed later stages
		"curl": hDownloader, "wget": hDownloader, "fetch": hDownloader, "http": hDownloader, "https": hDownloader,
		"invoke-webrequest": hDownloader, "invoke-restmethod": hDownloader, "aria2c": hDownloader,
		"base64": hDecoder, "xxd": hDecoder, "openssl": hDecoder,
		"pgrep": hByName, "pidof": hByName, "get-process": hGetProcess, "ps": hPs, "grep": hGrep,
		"egrep": hGrep, "rg": hGrep, "findstr": hGrep, "select-string": hGrep, "get-childitem": hGCI,
		"get-ciminstance": hGetCim, "get-wmiobject": hGetCim,
		// other
		"git": hGit, "tmx": hTmx, "tmxg": hTmx,
		"open": hOpen, "xdg-open": hDesktopArgs, "gnome-open": hDesktopArgs, "kde-open": hDesktopArgs,
		"kde-open5": hDesktopArgs, "wslview": hDesktopArgs, "cygstart": hDesktopArgs, "gio": hGio,
		"explorer": hDesktopArgs, "invoke-item": hDesktopArgs, "start": hStart, "start-process": hStartProcess,
		"rundll32": hRundll,
	}
}

// prefixHandler covers families named by prefix (mkfs.ext4, newfs_apfs).
func prefixHandler(verb string) handler {
	switch {
	case strings.HasPrefix(verb, "mkfs"), strings.HasPrefix(verb, "newfs"):
		return hDiskAlways
	}
	return nil
}

// ---- helpers ------------------------------------------------------------------------------

// isOpt reports whether s is an option (cmdStyle: also /x flags of cmd.exe programs).
func isOpt(s string, cmdStyle bool) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] == '-' {
		return true
	}
	return cmdStyle && s[0] == '/' && cmdFlag.MatchString(s)
}

var cmdFlag = regexp.MustCompile(`^/[a-zA-Z?]{1,3}(:\S*)?$`)

// positional splits a command's arguments into options (as written) and the rest. withValue
// names options that take the following word; PowerShell's path parameters (-Path x) count as
// positional paths.
func positional(c *call, cmdStyle bool, withValue map[string]bool) (opts []string, pos []arg) {
	ended := false
	for i := 1; i < len(c.args); i++ {
		x := c.args[i]
		s := x.s
		if !ended && s == "--" && c.sh != ShellCmd {
			ended = true
			continue
		}
		if ended || x.unknownStart || !isOpt(s, cmdStyle) {
			pos = append(pos, x)
			continue
		}
		name := s
		if k := strings.IndexByte(name, ':'); k > 0 && c.sh == ShellPowerShell {
			name = name[:k] // -Confirm:$false
		}
		opts = append(opts, name)
		l := strings.ToLower(name)
		if c.sh == ShellPowerShell && psPath[l] && i+1 < len(c.args) {
			pos = append(pos, c.args[i+1])
			i++
			continue
		}
		if (withValue[name] || withValue[l]) && i+1 < len(c.args) {
			i++
		}
	}
	return opts, pos
}

var psPath = map[string]bool{"-path": true, "-literalpath": true, "-lp": true, "-pspath": true, "-filepath": true}

var psValue = map[string]bool{"-include": true, "-exclude": true, "-filter": true, "-erroraction": true,
	"-ea": true, "-credential": true, "-stream": true, "-itemtype": true, "-type": true, "-value": true,
	"-encoding": true, "-name": true, "-warningaction": true, "-wa": true, "-depth": true, "-newname": true,
	"-destination": true, "-target": true}

func merge(ms ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// hasOpt reports whether one of names was given (case-insensitive).
func hasOpt(opts []string, names ...string) bool {
	for _, o := range opts {
		for _, n := range names {
			if strings.EqualFold(o, n) {
				return true
			}
		}
	}
	return false
}

// shortCluster reports whether a POSIX short-option cluster (-rf) contains letter.
func shortCluster(opts []string, letters string) bool {
	for _, o := range opts {
		if strings.HasPrefix(o, "--") || !strings.HasPrefix(o, "-") || len(o) > 6 {
			continue
		}
		if strings.ContainsAny(o[1:], letters) {
			return true
		}
	}
	return false
}

func (a *analyzer) read(t target, match string) {
	if !t.unknown && a.stateSecret(t.p) {
		a.add(RuleTmxState, "reads tmx's secrets ("+t.shown+")", match)
	}
}

func (a *analyzer) write(t target, match string) {
	if t.unknown || t.p == "" || isNullDevice(t.p) || isNullDevice(t.shown) {
		return
	}
	p := t.p
	switch {
	case deviceRx.MatchString(p) || strings.HasPrefix(p, "//./physicaldrive"):
		a.add(RuleDisk, "writes straight to the disk device "+t.shown, match)
	case a.stateSecret(p):
		a.add(RuleTmxState, "writes into tmx's secrets or its own settings ("+t.shown+")", match)
	case a.claudeSettingsFile(p): // before inState: the agents' CLAUDE_CONFIG_DIR is in tmx's state
		a.add(RuleClaudeConfig, "writes the Claude Code settings file "+t.shown, match)
		if !a.fileTool {
			a.add(RuleClaudeHooks, "a shell write, delete or rename of the Claude Code settings file "+t.shown+" (it can't be checked for the guard hook)", match)
		}
	case a.inState(p):
		a.add(RuleTmxState, "writes into tmx's state folder ("+t.shown+")", match)
	case within(p, a.own) || within(p, a.workspace) || a.inTemp(p) || a.claudeData(p):
	default:
		a.add(RuleWriteOutside, "writes "+t.shown+", outside the agent's folder and the workspace", match)
	}
}

// del checks a delete of t (recursive: the whole tree).
func (a *analyzer) del(t target, recursive bool, match string) {
	if t.unknown {
		if recursive {
			a.add(RuleDeleteOutside, "deletes "+t.shown+" recursively, a path only known when the command runs", match)
		}
		return
	}
	p := t.p
	if p == "" {
		return
	}
	// Any delete of a Claude settings file, and a recursive delete of a folder holding them, before
	// the state branch below returns: claude-config is inside tmx's state (go-reviewer-o4).
	if a.claudeSettingsFile(p) || recursive && (strings.HasSuffix(p, "/.claude") || within(p, a.state+"/claude-config")) {
		a.add(RuleClaudeHooks, "deletes Claude Code's settings ("+t.shown+")", match)
	}
	if a.inState(p) || a.holdsState(p) && a.protected(p) == "" {
		what := " (tmx's state)"
		if a.stateSecret(p) || a.holdsState(p) {
			what = " (tmx's state with its secrets)"
		}
		a.add(RuleTmxState, "deletes "+t.shown+what, match)
		return
	}
	if !recursive {
		a.write(t, match)
		return
	}
	if why := a.protected(p); why != "" {
		what := "deletes " + why
		if t.glob {
			what = "deletes everything in " + why
		}
		a.add(RuleDeleteRoot, what+" recursively", match)
		return
	}
	switch {
	case a.inTemp(p), inside(p, a.own), a.claudeData(p):
	case p == a.own:
		a.add(RuleDeleteOutside, "deletes the agent's whole folder ("+t.shown+")", match)
	case p == a.workspace:
		a.add(RuleDeleteOutside, "deletes the whole workspace ("+t.shown+")", match)
	case a.userContent(p): // paths.go: the owner's own folders are ordinary work
	default:
		a.add(RuleDeleteOutside, "deletes "+t.shown+" recursively, outside the agent's folder", match)
	}
}

// ---- deletes, moves, writes ----------------------------------------------------------------

func hDelete(a *analyzer, c *call) {
	cmdStyle := c.verb == "rd" || c.verb == "del" || c.verb == "erase" || c.verb == "rmdir"
	opts, pos := positional(c, cmdStyle, psValue)
	recursive := hasOpt(opts, "--recursive", "/s") || shortCluster(opts, "rR")
	for _, o := range opts {
		if l := strings.ToLower(o); len(l) >= 4 && strings.HasPrefix("-recurse", l) {
			recursive = true
		}
		if o == "--no-preserve-root" {
			a.add(RuleDeleteRoot, "rm --no-preserve-root deletes even /", c.raw)
		}
	}
	if c.verb == "rmdir" && !hasOpt(opts, "/s") && c.sh != ShellPowerShell {
		recursive = false // rmdir removes empty folders only
	}
	if c.verb == "shred" || c.verb == "srm" {
		recursive = recursive && c.verb == "srm"
	}
	if c.verb == "remove-item" && c.upstream(func(s *stage) bool { return s.recurse }) {
		recursive = true
	}
	if len(pos) == 0 && c.verb == "remove-item" && len(c.up) > 0 {
		a.del(target{unknown: true, shown: "(the items piped in)"}, recursive, c.raw)
	}
	for _, x := range pos {
		a.del(a.target(x), recursive, c.raw)
	}
}

// explicitDest writes the value of -t / --target-directory / -Destination and reports whether
// there was one.
func explicitDest(a *analyzer, c *call) bool {
	found := false
	for i := 1; i < len(c.args)-1; i++ {
		s := c.args[i].s
		if s == "-t" || s == "--target-directory" || strings.EqualFold(s, "-destination") {
			a.write(a.target(c.args[i+1]), c.raw)
			found = true
		}
	}
	return found
}

func hMove(a *analyzer, c *call) {
	_, pos := positional(c, c.verb == "move" || c.verb == "ren", merge(psValue, map[string]bool{"-t": true, "--target-directory": true}))
	if len(pos) == 0 {
		return
	}
	srcs := pos
	if !explicitDest(a, c) && len(pos) >= 2 {
		srcs = pos[:len(pos)-1]
		a.write(a.target(pos[len(pos)-1]), c.raw)
	}
	for _, x := range srcs {
		t := a.target(x)
		if !t.unknown {
			if why := a.protected(t.p); why != "" {
				a.add(RuleDeleteRoot, "moves "+why+" away", c.raw)
				continue
			}
			if a.holdsState(t.p) {
				a.add(RuleTmxState, "moves "+t.shown+" (tmx's state with its secrets)", c.raw)
				continue
			}
		}
		a.write(t, c.raw)
	}
}

func hCopy(a *analyzer, c *call) {
	withValue := merge(psValue, map[string]bool{"-m": true, "-o": true, "-g": true, "-e": true,
		"--exclude": true, "--include": true, "-t": true, "--target-directory": true, "--mode": true,
		"--owner": true, "--group": true})
	opts, pos := positional(c, c.verb == "copy" || c.verb == "xcopy" || c.verb == "robocopy", withValue)
	hasDest := explicitDest(a, c)
	remote := func(x arg) bool {
		i := strings.IndexByte(x.s, ':')
		return i > 1 && !strings.ContainsAny(x.s[:i], `/\`)
	}
	var srcs []arg
	var dest *arg
	switch {
	case c.verb == "robocopy" && len(pos) >= 2:
		srcs, dest = pos[:1], &pos[1]
	case hasDest:
		srcs = pos
	case c.verb == "install" && hasOpt(opts, "-d"):
		for i := range pos {
			a.write(a.target(pos[i]), c.raw)
		}
		return
	case len(pos) >= 2:
		srcs, dest = pos[:len(pos)-1], &pos[len(pos)-1]
	}
	for _, x := range srcs {
		if remote(x) {
			continue
		}
		if t := a.target(x); !t.unknown && a.state != "" && within(a.state+"/secrets", t.p) {
			a.add(RuleTmxState, "copies tmx's secrets ("+t.shown+")", c.raw)
		}
	}
	if dest == nil || remote(*dest) {
		return
	}
	t := a.target(*dest)
	mirror := hasOpt(opts, "--delete", "--delete-after", "--delete-before", "--delete-during", "--del",
		"/mir", "/purge")
	if mirror {
		a.del(t, true, c.raw)
		return
	}
	a.write(t, c.raw)
}

func hWriteArgs(a *analyzer, c *call) {
	withValue := merge(psValue, map[string]bool{"-s": true, "--size": true, "-m": true, "--mode": true,
		"-r": c.verb == "touch", "-d": c.verb == "touch", "-t": c.verb == "touch"})
	_, pos := positional(c, c.verb == "md" && c.sh == ShellCmd, withValue)
	if c.verb == "new-item" || c.verb == "set-content" || c.verb == "add-content" || c.verb == "out-file" {
		if len(pos) > 1 {
			pos = pos[:1] // Set-Content path value
		}
	}
	for _, x := range pos {
		a.write(a.target(x), c.raw)
	}
}

func hDD(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if v, ok := strings.CutPrefix(x.s, "of="); ok {
			y := x
			y.s, y.raw = v, strings.TrimPrefix(x.raw, "of=")
			a.write(a.target(y), c.raw)
		}
	}
}

func hArchive(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if t := a.target(x); !t.unknown && a.state != "" && within(a.state+"/secrets", t.p) && looksLikePath(x.s) {
			a.add(RuleTmxState, "archives tmx's secrets ("+t.shown+")", c.raw)
		}
	}
}

// ---- permissions ----------------------------------------------------------------------------

func hPerms(a *analyzer, c *call) {
	withValue := map[string]bool{"-m": c.verb == "setfacl", "-x": c.verb == "setfacl", "-M": c.verb == "setfacl",
		"--reference": false}
	opts, pos := positional(c, false, withValue)
	recursive := hasOpt(opts, "--recursive") || shortCluster(opts, "R")
	modeAsOpt := false // chmod -x file: the mode looks like an option
	for _, o := range opts {
		if c.verb == "chmod" && chmodMinus.MatchString(o) {
			modeAsOpt = true
		}
	}
	if c.verb != "setfacl" && !modeAsOpt && !hasOpt(opts, "--reference") && len(pos) > 0 {
		pos = pos[1:] // the mode, owner or flags
	}
	for _, x := range pos {
		a.perm(a.target(x), recursive, false, c.raw)
	}
}

var chmodMinus = regexp.MustCompile(`^-[rwxXst]+$`)

var aclWrite = regexp.MustCompile(`(?i)^/(grant|deny|remove|reset|setowner|inheritance|restore|setintegritylevel|substitute)`)

func hACL(a *analyzer, c *call) {
	var targets []arg
	recursive, writes := false, false
	switch c.verb {
	case "icacls", "cacls", "xcacls", "subinacl", "setacl":
		if c.verb == "setacl" { // SetACL.exe -on <object> -ot <type> -actn ...
			for i := 1; i+1 < len(c.args); i++ {
				if strings.EqualFold(c.args[i].s, "-on") {
					targets = append(targets, c.args[i+1])
				}
			}
			if len(targets) > 0 {
				writes = true
				break
			}
		}
		for i, x := range c.args[1:] {
			l := strings.ToLower(x.s)
			switch {
			case l == "/t" || l == "/r" && c.verb != "icacls" || l == "/s":
				recursive = true
			case aclWrite.MatchString(l) || c.verb != "icacls" && (l == "/e" || l == "/g" || l == "/p" || l == "/d" || l == "/r"):
				writes = true
			case i == 0 || !strings.HasPrefix(l, "/") && len(targets) == 0:
				targets = append(targets, x)
			}
		}
		if c.verb != "icacls" {
			writes = true
		}
	case "takeown":
		writes = true
		for i := 1; i < len(c.args); i++ {
			l := strings.ToLower(c.args[i].s)
			if l == "/r" {
				recursive = true
			}
			if l == "/f" && i+1 < len(c.args) {
				targets = append(targets, c.args[i+1])
			}
		}
	case "set-acl":
		writes = true
		_, pos := positional(c, false, merge(psValue, map[string]bool{"-aclobject": true}))
		if len(pos) > 0 {
			targets = pos[:1]
		}
		recursive = c.upstream(func(s *stage) bool { return s.recurse })
		if len(targets) == 0 && len(c.up) > 0 {
			targets = []arg{{unknownStart: true, raw: "(the items piped in)"}}
		}
	}
	if !writes {
		return
	}
	for _, x := range targets {
		a.perm(a.target(x), recursive, true, c.raw)
	}
}

func (a *analyzer) perm(t target, recursive, acl bool, match string) {
	if t.unknown {
		if recursive {
			a.add(RulePermsOutside, "changes permissions of "+t.shown+", a path only known when the command runs", match)
		}
		return
	}
	p := t.p
	if p == "" {
		return
	}
	if a.inState(p) {
		what := "changes permissions in tmx's state folder"
		if a.stateSecret(p) {
			what = "changes permissions on tmx's secrets"
		}
		a.add(RuleTmxState, what, match)
		return
	}
	if why := a.protected(p); why != "" {
		if recursive || acl {
			a.add(RulePermsRoot, "changes the permissions of "+why+map[bool]string{true: " recursively", false: ""}[recursive], match)
		} else {
			a.add(RulePermsOutside, "changes the permissions of "+why, match)
		}
		return
	}
	if within(p, a.own) || within(p, a.workspace) || a.inTemp(p) {
		return
	}
	a.add(RulePermsOutside, "changes permissions of "+t.shown+", outside the agent's folder and the workspace", match)
}

// ---- disks and boot ---------------------------------------------------------------------------

var diskutilVerbs = regexp.MustCompile(`(?i)^(erase|zero|random|secureerase|partition|split|merge|resize|delete|reformat|addpartition|repairdisk)`)

func hDiskutil(a *analyzer, c *call) {
	sub := ""
	for i, x := range c.args[1:] {
		s := x.s
		if strings.HasPrefix(s, "-") {
			continue
		}
		sub = s
		if strings.EqualFold(sub, "apfs") || strings.EqualFold(sub, "cs") || strings.EqualFold(sub, "ar") {
			if i+2 < len(c.args) {
				sub = c.args[i+2].s
			}
		}
		break
	}
	if diskutilVerbs.MatchString(sub) {
		a.add(RuleDisk, "diskutil "+sub+" erases or repartitions a disk", c.raw)
	}
}

func hDiskAlways(a *analyzer, c *call) {
	a.add(RuleDisk, c.verb+" formats, wipes or repartitions a disk", c.raw)
}

func hPartition(a *analyzer, c *call) {
	device := false
	for _, x := range c.args[1:] {
		l := strings.ToLower(x.s)
		switch l {
		case "-l", "--list", "-s", "--show", "print", "-p", "--print", "-v", "--version", "-h", "--help", "-d", "--dump":
			return
		}
		if strings.HasPrefix(l, "/dev/") {
			device = true
		}
	}
	if device {
		a.add(RuleDisk, c.verb+" edits a disk's partition table", c.raw)
	}
}

var driveArg = regexp.MustCompile(`(?i)^[a-z]:\\?$`)

func hFormat(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if driveArg.MatchString(x.s) || strings.HasPrefix(strings.ToLower(x.s), "/fs:") {
			a.add(RuleDisk, "format erases the drive "+strings.ToUpper(x.s), c.raw)
			return
		}
	}
}

func hCipher(a *analyzer, c *call) {
	enc, sub := false, false
	for _, x := range c.args[1:] {
		l := strings.ToLower(x.s)
		if strings.HasPrefix(l, "/w") {
			a.add(RuleDisk, "cipher /w wipes free disk space", c.raw)
			return
		}
		enc = enc || l == "/e"
		sub = sub || strings.HasPrefix(l, "/s")
	}
	if enc && sub {
		a.add(RuleDisk, "cipher /e /s encrypts a whole tree", c.raw)
	}
}

func hVSS(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "delete" || l == "resize" {
			a.add(RuleDisk, c.verb+" "+l+" deletes restore points or backups", c.raw)
			return
		}
	}
}

func hWMIC(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if strings.Contains(j, "shadowcopy") && strings.Contains(j, "delete") {
		a.add(RuleDisk, "wmic shadowcopy delete deletes restore points", c.raw)
	}
	if strings.Contains(j, "process") && strings.Contains(j, "name") &&
		(strings.Contains(j, "delete") || strings.Contains(j, "terminate")) {
		a.add(RuleKillByName, "wmic kills processes by name", c.raw)
	}
}

func hASR(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "restore" || l == "--erase" || l == "-erase" {
			a.add(RuleDisk, "asr restore overwrites a volume", c.raw)
			return
		}
	}
}

func hBootAlways(a *analyzer, c *call) {
	a.add(RuleBoot, c.verb+" changes how the machine boots", c.raw)
}

func hCsrutil(a *analyzer, c *call) {
	if len(c.args) > 1 && strings.EqualFold(c.args[1].s, "status") {
		return
	}
	a.add(RuleBoot, "csrutil changes System Integrity Protection", c.raw)
}

func hBcdedit(a *analyzer, c *call) {
	if len(c.args) == 1 {
		return
	}
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "/enum" || l == "/v" || l == "/?" {
			return
		}
	}
	a.add(RuleBoot, "bcdedit changes the boot configuration", c.raw)
}

func hNvram(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		s := x.s
		if s == "-d" || s == "-c" || s == "-f" || strings.Contains(s, "=") {
			a.add(RuleBoot, "nvram changes firmware variables", c.raw)
			return
		}
	}
}

func hReagentc(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "/disable" || l == "/setreimage" || l == "/boottore" {
			a.add(RuleBoot, "reagentc changes the Windows recovery environment", c.raw)
			return
		}
	}
}

func hMountvol(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "/d" || l == "/p" || l == "/s" || l == "/r" || strings.HasPrefix(l, `\\?\volume`) {
			a.add(RuleBoot, "mountvol mounts or removes a volume (such as the boot partition)", c.raw)
			return
		}
	}
}

func hEfiboot(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		switch x.s {
		case "-c", "--create", "-b", "--bootnum", "-B", "--delete-bootnum", "-o", "--bootorder", "-n",
			"--bootnext", "-a", "--active", "-A", "--inactive", "-O", "--delete-bootorder":
			a.add(RuleBoot, "efibootmgr changes the firmware boot entries", c.raw)
			return
		}
	}
}

func hSpctl(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "--master-disable" || l == "--global-disable" || l == "--disable" {
			a.add(RuleBoot, "spctl switches off Gatekeeper", c.raw)
			return
		}
	}
}

// ---- firewall and Tailscale ----------------------------------------------------------------------

func hNetAlways(a *analyzer, c *call) {
	a.add(RuleNetwork, c.verb+" changes the firewall", c.raw)
}

// hNetsh: only its firewall contexts; "show" reads.
func hNetsh(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if (strings.Contains(j, "advfirewall") || strings.Contains(j, "firewall")) && !strings.Contains(j, " show") {
		a.add(RuleNetwork, "netsh changes the firewall", c.raw)
	}
}

func hUfw(a *analyzer, c *call) {
	if len(c.args) > 1 && strings.EqualFold(c.args[1].s, "status") {
		return
	}
	a.add(RuleNetwork, "ufw changes the firewall", c.raw)
}

// tailscaleRead are Tailscale subcommands that only read.
var tailscaleRead = map[string]bool{"status": true, "ip": true, "ping": true, "version": true, "whois": true,
	"netcheck": true, "dns": true, "exit-node": false, "bugreport": true, "metrics": true, "licenses": true}

func hTailscale(a *analyzer, c *call) {
	sub := ""
	for _, x := range c.args[1:] {
		if !strings.HasPrefix(x.s, "-") {
			sub = strings.ToLower(x.s)
			break
		}
	}
	if sub == "" || tailscaleRead[sub] {
		return
	}
	// "serve status" / "funnel status" only read
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if (sub == "serve" || sub == "funnel") && strings.HasSuffix(strings.TrimSpace(j), " status") {
		return
	}
	a.add(RuleNetwork, "tailscale "+sub+" changes how this machine is reached", c.raw)
}

// hTailscaleService: stopping, suspending or disabling the Tailscale service (Stop-Service,
// Set-Service -StartupType Disabled / -Status Stopped, sc.exe stop/config/delete, net stop).
func hTailscaleService(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if !strings.Contains(j, "tailscale") {
		return
	}
	switch c.verb {
	case "set-service":
		if !strings.Contains(j, "disabled") && !strings.Contains(j, "stopped") && !strings.Contains(j, "manual") {
			return
		}
	case "sc.exe", "sc":
		if !strings.Contains(j, "stop") && !strings.Contains(j, "config") && !strings.Contains(j, "delete") {
			return
		}
	case "net", "net.exe":
		if !strings.HasPrefix(j, "stop") && !strings.Contains(j, " stop") {
			return
		}
	}
	a.add(RuleNetwork, c.verb+" stops or disables the Tailscale service", c.raw)
}

// ---- Windows Hello and security keys -------------------------------------------------------------

func hHelloAlways(a *analyzer, c *call) {
	a.add(RuleHelloKey, c.verb+" talks to a security key / Windows Hello", c.raw)
}

var (
	skType = regexp.MustCompile(`(?i)^(ecdsa-sk|ed25519-sk|sk-[\w@.-]+)$`)
	skFile = regexp.MustCompile(`(?i)(^|[\\/])id_(ecdsa|ed25519)_sk(\.pub)?$|_sk(\.pub)?$`)
)

// hSSHKeygen: making or using a security-key (-sk) key reaches Windows Hello (14 Oct incident).
func hSSHKeygen(a *analyzer, c *call) {
	args := argStrings(c.args[1:])
	sign, skKey := false, false
	for i, x := range args {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch {
		case x == "-t" && skType.MatchString(next), strings.HasPrefix(x, "-t") && skType.MatchString(strings.TrimPrefix(x, "-t")):
			a.add(RuleHelloKey, "ssh-keygen makes a security-key (-sk) key", c.raw)
			return
		case x == "-K":
			a.add(RuleHelloKey, "ssh-keygen -K loads keys from a security key", c.raw)
			return
		case x == "-O" || strings.HasPrefix(x, "-O") && len(x) > 2:
			a.add(RuleHelloKey, "ssh-keygen -O sets security-key options", c.raw)
			return
		case x == "-Y" && (next == "sign" || next == "verify"):
			sign = true
		}
		if skFile.MatchString(x) {
			skKey = true
		}
	}
	if sign && skKey {
		a.add(RuleHelloKey, "ssh-keygen signs with a security-key (-sk) key", c.raw)
	}
}

// ---- processes and power ------------------------------------------------------------------------

func hPkill(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := x.s; l == "-l" || l == "-L" || l == "--list" || l == "-V" || l == "--version" || l == "-h" || l == "--help" {
			return
		}
	}
	a.add(RuleKillByName, c.verb+" kills every process with that name", c.raw)
}

func hTaskkill(a *analyzer, c *call) {
	for i := 1; i < len(c.args); i++ {
		l := strings.ToLower(c.args[i].s)
		if l == "/im" {
			a.add(RuleKillByName, "taskkill /IM kills every process with that name", c.raw)
			return
		}
		if l == "/fi" && i+1 < len(c.args) && strings.Contains(strings.ToLower(c.args[i+1].s), "imagename") {
			a.add(RuleKillByName, "taskkill /FI IMAGENAME kills processes by name", c.raw)
			return
		}
	}
}

// listsByName reports whether a command line lists processes by name (for kill $(...)).
func listsByName(src string) bool {
	ps, grep := false, false
	for _, c := range lex(src, ShellPOSIX) {
		if len(c.words) == 0 {
			continue
		}
		s, _ := c.words[0].literal()
		switch verbOf(s) {
		case "pgrep", "pidof", "pkill":
			return true
		case "ps", "get-process", "gps":
			ps = true
		case "grep", "egrep", "awk", "rg":
			grep = true
		}
	}
	return ps && grep
}

func hKill(a *analyzer, c *call) {
	if c.sh == ShellPowerShell {
		hStopProcess(a, c)
		return
	}
	for _, x := range c.args[1:] {
		for _, s := range x.subs {
			if listsByName(s) {
				a.add(RuleKillByName, "kill is given the PIDs of processes found by name", c.raw)
				return
			}
		}
	}
	i := 1 // kill [-SIGNAL | -s SIGNAL | -n NUM] [--] PID...
	if i < len(c.args) {
		switch s := c.args[i].s; {
		case s == "-l" || s == "-L":
			return
		case s == "-s" || s == "-n":
			i += 2
		case strings.HasPrefix(s, "-") && s != "--":
			i++
		}
	}
	if i < len(c.args) && c.args[i].s == "--" {
		i++
	}
	for _, x := range c.args[min(i, len(c.args)):] {
		if x.s == "-1" {
			a.add(RuleKillByName, "kill -1 signals every process of the owner", c.raw)
			return
		}
	}
	if c.upstream(func(s *stage) bool { return s.byName }) && xargsFed(c) {
		a.add(RuleKillByName, "kill is fed the PIDs of processes found by name", c.raw)
	}
}

// xargsFed: the call came through xargs (its last argument is the piped-in placeholder).
func xargsFed(c *call) bool {
	n := len(c.args)
	return n > 0 && c.args[n-1].raw == xargsInput
}

const xargsInput = "the paths xargs reads"

func hStopProcess(a *analyzer, c *call) {
	opts, pos := positional(c, false, map[string]bool{"-id": true, "-inputobject": true})
	if hasOpt(opts, "-name", "-processname", "-n") {
		a.add(RuleKillByName, "Stop-Process -Name kills every process with that name", c.raw)
		return
	}
	if len(pos) > 0 && !isDigits(pos[0].s) && !pos[0].unknown {
		a.add(RuleKillByName, "Stop-Process kills processes by name", c.raw)
		return
	}
	if c.upstream(func(s *stage) bool { return s.byName }) {
		a.add(RuleKillByName, "Stop-Process is fed processes found by name", c.raw)
	}
}

func hCimKill(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if c.verb == "invoke-cimmethod" && !strings.Contains(j, "terminate") {
		return
	}
	if c.upstream(func(s *stage) bool { return s.byName }) {
		a.add(RuleKillByName, "kills processes found by name", c.raw)
	}
}

func hXargs(a *analyzer, c *call) {
	withValue := map[string]bool{"-n": true, "-I": true, "-L": true, "-P": true, "-s": true, "-d": true,
		"-E": true, "-a": true, "--max-args": true, "--max-procs": true, "--delimiter": true, "--arg-file": true}
	i := 1
	for i < len(c.args) {
		s := c.args[i].s
		if !strings.HasPrefix(s, "-") || s == "-" {
			break
		}
		if s == "--" {
			i++
			break
		}
		if withValue[s] {
			i++
		}
		i++
	}
	if i >= len(c.args) {
		return
	}
	inner := append([]arg{}, c.args[i:]...)
	var roots []string
	for _, s := range c.up {
		roots = append(roots, s.roots...)
	}
	if len(roots) > 0 {
		for _, r := range roots {
			inner = append(inner, arg{s: r + "/x", raw: "what find matches under " + r})
		}
	} else {
		inner = append(inner, arg{unknownStart: true, unknown: true, raw: xargsInput})
	}
	a.dispatch(inner, c.raw, c.sh, c.up)
}

func hShutdown(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if l := strings.ToLower(x.s); l == "/a" || l == "-c" || l == "--help" || l == "/?" || l == "-a" {
			return
		}
	}
	a.add(RuleShutdown, "shutdown turns the machine off or restarts it", c.raw)
}

func hPowerAlways(a *analyzer, c *call) {
	a.add(RuleShutdown, c.verb+" turns the machine off or restarts it", c.raw)
}

func hInit(a *analyzer, c *call) {
	if len(c.args) > 1 && (c.args[1].s == "0" || c.args[1].s == "6") {
		a.add(RuleShutdown, c.verb+" "+c.args[1].s+" turns the machine off or restarts it", c.raw)
	}
}

func hSystemctl(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		switch strings.ToLower(x.s) {
		case "poweroff", "reboot", "halt", "kexec", "suspend", "hibernate", "hybrid-sleep",
			"suspend-then-hibernate", "soft-reboot", "emergency", "rescue":
			a.add(RuleShutdown, "systemctl "+x.s+" stops the machine", c.raw)
			return
		}
	}
}

func hLaunchctl(a *analyzer, c *call) {
	if len(c.args) < 2 {
		return
	}
	switch strings.ToLower(c.args[1].s) {
	case "reboot":
		a.add(RuleShutdown, "launchctl reboot restarts the machine", c.raw)
	case "bootout":
		if len(c.args) > 2 && (strings.HasPrefix(c.args[2].s, "gui/") || strings.HasPrefix(c.args[2].s, "user/")) &&
			!strings.Contains(strings.TrimPrefix(strings.TrimPrefix(c.args[2].s, "gui/"), "user/"), "/") {
			a.add(RuleShutdown, "launchctl bootout logs the owner out", c.raw)
		}
	}
}

func hLoginctl(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		switch strings.ToLower(x.s) {
		case "terminate-user", "kill-user", "terminate-session", "kill-session", "poweroff", "reboot", "suspend", "hibernate":
			a.add(RuleShutdown, "loginctl "+x.s+" logs the owner out or stops the machine", c.raw)
			return
		}
	}
}

var (
	osaAdmin    = regexp.MustCompile(`(?i)with\s+administrator\s+privileges`)
	osaPower    = regexp.MustCompile(`(?i)\b(shut\s*down|restart|log\s*out)\b`)
	osaShell    = regexp.MustCompile(`(?i)do\s+shell\s+script\s+"((?:[^"\\]|\\.)*)"`)
	osaActivate = regexp.MustCompile(`(?i)\b(activate|open\s+location|open\s+(file|folder|POSIX))\b`)
)

func hOsascript(a *analyzer, c *call) {
	text := strings.Join(argStrings(c.args[1:]), " ")
	if osaAdmin.MatchString(text) {
		a.add(RuleElevation, "osascript runs a command with administrator privileges", c.raw)
	}
	if osaPower.MatchString(text) && strings.Contains(strings.ToLower(text), "system events") {
		a.add(RuleShutdown, "osascript asks System Events to shut down, restart or log out", c.raw)
	}
	for _, m := range osaShell.FindAllStringSubmatch(text, -1) {
		a.nested(strings.ReplaceAll(m[1], `\"`, `"`), ShellPOSIX)
	}
	if osaActivate.MatchString(text) {
		a.add(RuleDesktop, "osascript brings an app or a document to the front", c.raw)
	}
}

// ---- shells, interpreters, evaluation ---------------------------------------------------------

func isShell(verb string) bool {
	switch verb {
	case "sh", "bash", "zsh", "dash", "ksh", "mksh", "fish", "csh", "tcsh", "ash":
		return true
	}
	return false
}

// downloadedSub reports whether a word runs a downloader in a substitution: "$(curl ...)".
func downloadedSub(x arg) bool {
	for _, s := range append(append([]string{}, x.subs...), x.procs...) {
		for _, c := range lex(s, ShellPOSIX) {
			if len(c.words) > 0 {
				if v, ok := c.words[0].literal(); ok && isDownloader(verbOf(v)) {
					return true
				}
			}
		}
	}
	return false
}

func isDownloader(v string) bool {
	switch v {
	case "curl", "wget", "fetch", "http", "https", "iwr", "irm", "invoke-webrequest", "invoke-restmethod", "aria2c":
		return true
	}
	return false
}

func feedsFromDownload(c *call) bool { return c.upstream(func(s *stage) bool { return s.downloads }) }

func hShell(a *analyzer, c *call) {
	scriptFile := func(x arg) {
		if downloadedSub(x) {
			a.add(RulePipeShell, c.verb+" runs a downloaded script", c.raw)
		}
	}
	fromStdin := false // -s: commands from stdin, the rest are its arguments
args:
	for i := 1; i < len(c.args); i++ {
		x := c.args[i]
		s := x.s
		switch {
		case s == "--":
			if !fromStdin && i+1 < len(c.args) {
				scriptFile(c.args[i+1])
				return
			}
			break args
		case s == "-" || s == "-s":
			fromStdin = true
		case len(s) > 1 && (s[0] == '-' || s[0] == '+') && !strings.HasPrefix(s, "--"):
			if strings.ContainsRune(s[1:], 'c') {
				if i+1 < len(c.args) {
					cmd := c.args[i+1]
					if downloadedSub(cmd) {
						a.add(RulePipeShell, c.verb+" -c runs downloaded code", c.raw)
					}
					a.nested(cmd.s, ShellPOSIX)
				}
				return
			}
			if s == "-o" || s == "+o" || s == "-O" || s == "+O" {
				i++
			}
		case strings.HasPrefix(s, "--"):
			if s == "--rcfile" || s == "--init-file" {
				i++
			}
		default:
			scriptFile(x)
			return
		}
	}
	// no script: it reads its commands from stdin
	if feedsFromDownload(c) {
		a.add(RulePipeShell, "downloaded text is piped into "+c.verb, c.raw)
	}
}

func hInterp(a *analyzer, c *call) {
	script := false
	for _, x := range c.args[1:] {
		if x.s == "-" {
			break
		}
		if strings.HasPrefix(x.s, "-") {
			if x.s == "-c" || x.s == "-e" || x.s == "--eval" || x.s == "-m" || x.s == "-p" || x.s == "-r" {
				script = true
				break
			}
			continue
		}
		script = true
		if downloadedSub(x) {
			a.add(RulePipeShell, c.verb+" runs a downloaded script", c.raw)
		}
		break
	}
	if !script && feedsFromDownload(c) {
		a.add(RulePipeShell, "downloaded text is piped into "+c.verb, c.raw)
	}
}

func hEval(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		if downloadedSub(x) {
			a.add(RulePipeShell, "eval runs downloaded code", c.raw)
		}
	}
	a.nested(strings.Join(argStrings(c.args[1:]), " "), c.sh)
}

func hSource(a *analyzer, c *call) {
	if len(c.args) < 2 {
		return
	}
	x := c.args[1]
	if downloadedSub(x) || (x.s == "/dev/stdin" || x.s == "-") && feedsFromDownload(c) {
		a.add(RulePipeShell, c.verb+" runs downloaded code", c.raw)
	}
}

func hIEX(a *analyzer, c *call) {
	if feedsFromDownload(c) {
		a.add(RulePipeShell, "downloaded text is piped into Invoke-Expression", c.raw)
	}
	for _, x := range c.args[1:] {
		if downloadedSub(x) {
			a.add(RulePipeShell, "Invoke-Expression runs downloaded code", c.raw)
		}
	}
	_, pos := positional(c, false, map[string]bool{"-command": false})
	a.nested(strings.Join(argStrings(pos), " "), ShellPowerShell)
}

func hCmd(a *analyzer, c *call) {
	for i := 1; i < len(c.args); i++ {
		l := strings.ToLower(c.args[i].s)
		if l == "/c" || l == "/k" || l == "//c" || l == "//k" || l == "/r" {
			a.nested(strings.Join(argStrings(c.args[i+1:]), " "), ShellCmd)
			return
		}
	}
	if feedsFromDownload(c) {
		a.add(RulePipeShell, "downloaded text is piped into cmd", c.raw)
	}
}

func hPowerShell(a *analyzer, c *call) {
	for i := 1; i < len(c.args); i++ {
		l := strings.ToLower(c.args[i].s)
		switch {
		case l == "-command" || l == "-c" || l == "/c" || l == "-com" || l == "-comm":
			if i+1 < len(c.args) {
				if c.args[i+1].s == "-" && feedsFromDownload(c) {
					a.add(RulePipeShell, "downloaded text is piped into "+c.verb, c.raw)
				}
				a.nested(strings.Join(argStrings(c.args[i+1:]), " "), ShellPowerShell)
			}
			return
		case l == "-encodedcommand" || l == "-enc" || l == "-e" || l == "-ec" || l == "-en":
			if i+1 < len(c.args) {
				if src, ok := decodeUTF16Base64(c.args[i+1].s); ok {
					a.nested(src, ShellPowerShell)
				} else {
					a.add(RuleDeleteOutside, "runs an encoded PowerShell command tmx cannot read", c.raw)
				}
			}
			return
		case l == "-file" || l == "-f":
			return
		case strings.HasPrefix(l, "-"):
			if l == "-executionpolicy" || l == "-ep" || l == "-windowstyle" || l == "-w" || l == "-configurationname" {
				i++
			}
		default:
			a.nested(strings.Join(argStrings(c.args[i:]), " "), ShellPowerShell)
			return
		}
	}
	if feedsFromDownload(c) {
		a.add(RulePipeShell, "downloaded text is piped into "+c.verb, c.raw)
	}
}

func decodeUTF16Base64(s string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u)), true
}

func argStrings(args []arg) []string {
	out := make([]string, len(args))
	for i, x := range args {
		out[i] = x.s
	}
	return out
}

// ---- find, cd --------------------------------------------------------------------------------

func hFind(a *analyzer, c *call) {
	i := 1
	for i < len(c.args) && (c.args[i].s == "-H" || c.args[i].s == "-L" || c.args[i].s == "-P" || c.args[i].s == "-E" || c.args[i].s == "-X" || c.args[i].s == "-x" || c.args[i].s == "-s") {
		i++
	}
	var roots []target
	for i < len(c.args) {
		s := c.args[i].s
		if strings.HasPrefix(s, "-") || s == "(" || s == "!" || s == `\(` {
			break
		}
		roots = append(roots, a.target(c.args[i]))
		i++
	}
	if len(roots) == 0 {
		roots = []target{{p: a.cwd, shown: "."}}
	}
	for _, r := range roots {
		if !r.unknown && r.p != "" {
			c.st.roots = append(c.st.roots, r.p)
		}
	}
	child := func(r target) target {
		if r.unknown || r.p == "" {
			return r
		}
		return target{p: strings.TrimSuffix(r.p, "/") + "/x", shown: "what find matches under " + r.shown}
	}
	for ; i < len(c.args); i++ {
		switch c.args[i].s {
		case "-delete":
			for _, r := range roots {
				a.del(child(r), true, c.raw)
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(c.args) && c.args[j].s != ";" && c.args[j].s != "+" {
				j++
			}
			inner := c.args[i+1 : j]
			i = j
			if len(inner) == 0 {
				continue
			}
			for _, r := range roots {
				cmd := make([]arg, len(inner))
				for k, x := range inner {
					cmd[k] = x
					if strings.Contains(x.s, "{}") {
						ch := child(r)
						cmd[k] = arg{s: strings.ReplaceAll(x.s, "{}", ch.p), raw: x.raw, unknownStart: ch.unknown, unknown: ch.unknown}
					}
				}
				a.dispatch(cmd, c.raw, c.sh, nil)
			}
		}
	}
}

func hCd(a *analyzer, c *call) {
	_, pos := positional(c, c.sh == ShellCmd, nil)
	if c.verb == "pushd" || c.verb == "push-location" {
		a.dirs = append(a.dirs, a.cwd)
	}
	if len(pos) == 0 {
		if c.sh == ShellPOSIX && (c.verb == "cd" || c.verb == "chdir") {
			a.cwd = a.home
		}
		return
	}
	if pos[0].s == "-" {
		return
	}
	t := a.target(pos[0])
	if !t.unknown && t.p != "" {
		a.cwd = t.p
	}
}

func hPopd(a *analyzer, c *call) {
	if n := len(a.dirs); n > 0 {
		a.cwd, a.dirs = a.dirs[n-1], a.dirs[:n-1]
	}
}

// ---- stages that feed later ones -------------------------------------------------------------

func hDownloader(a *analyzer, c *call) {
	c.st.downloads = true
	for i := 1; i < len(c.args); i++ {
		if l := c.args[i].s; (l == "-o" || l == "--output" || l == "-O" && c.verb == "wget" || strings.EqualFold(l, "-outfile")) && i+1 < len(c.args) {
			if c.args[i+1].s != "-" {
				c.st.downloads = false
				a.write(a.target(c.args[i+1]), c.raw)
			}
		}
	}
	if len(c.up) > 0 {
		if prev := c.up[len(c.up)-1]; prev.verb == "invoke-expression" {
			a.add(RulePipeShell, "Invoke-Expression runs downloaded code", c.raw)
		}
	}
}

func hDecoder(a *analyzer, c *call) {
	for _, x := range c.args[1:] {
		switch x.s {
		case "-d", "-D", "--decode", "-r", "-revert":
			c.st.downloads = true
		}
	}
	if c.verb == "openssl" && len(c.args) > 1 && (c.args[1].s == "base64" || c.args[1].s == "enc") {
		for _, x := range c.args[2:] {
			if x.s == "-d" {
				c.st.downloads = true
			}
		}
	}
}

func hByName(a *analyzer, c *call) { c.st.byName = true }

func hPs(a *analyzer, c *call) {
	if c.sh == ShellPowerShell {
		hGetProcess(a, c)
	}
}

func hGrep(a *analyzer, c *call) {
	c.st.grep = true
	if c.upstream(func(s *stage) bool { return s.verb == "ps" }) {
		c.st.byName = true
	}
}

func hGetProcess(a *analyzer, c *call) {
	opts, _ := positional(c, false, map[string]bool{"-id": true})
	c.st.byName = !hasOpt(opts, "-id")
}

func hGetCim(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	c.st.byName = strings.Contains(j, "win32_process") && strings.Contains(j, "name")
}

func hGCI(a *analyzer, c *call) {
	opts, _ := positional(c, false, psValue)
	for _, o := range opts {
		if l := strings.ToLower(o); l == "-r" || len(l) >= 4 && strings.HasPrefix("-recurse", l) {
			c.st.recurse = true
		}
	}
}

// ---- tmx itself -------------------------------------------------------------------------------

var tmxAllowed = map[string]bool{"": true, "help": true, "-h": true, "--help": true, "version": true,
	"status": true, "ls": true, "approvals": true, "hook": true, "mcp": true}

func hTmx(a *analyzer, c *call) {
	sub := ""
	for _, x := range c.args[1:] {
		if !strings.HasPrefix(x.s, "-") || x.s == "-h" || x.s == "--help" {
			sub = strings.ToLower(x.s)
			break
		}
	}
	if !tmxAllowed[sub] {
		a.add(RuleTmxCLI, "tmx "+sub+" acts as the owner", c.raw)
	}
}

// ---- desktop windows ---------------------------------------------------------------------------

func hOpen(a *analyzer, c *call) {
	if len(c.args) < 2 {
		return
	}
	for _, x := range c.args[1:] {
		if x.s == "-h" || x.s == "--help" {
			return
		}
	}
	a.add(RuleDesktop, "open shows a document, folder, URL or app on the owner's screen", c.raw)
}

func hDesktopArgs(a *analyzer, c *call) {
	if len(c.args) < 2 && c.verb == "explorer" {
		return
	}
	a.add(RuleDesktop, c.verb+" opens a window on the owner's screen", c.raw)
}

func hGio(a *analyzer, c *call) {
	if len(c.args) > 1 && c.args[1].s == "open" {
		a.add(RuleDesktop, "gio open opens a window on the owner's screen", c.raw)
	}
}

func hRundll(a *analyzer, c *call) {
	j := strings.ToLower(strings.Join(argStrings(c.args[1:]), " "))
	if strings.Contains(j, "fileprotocolhandler") || strings.Contains(j, "openas_rundll") || strings.Contains(j, "shell32") {
		a.add(RuleDesktop, "rundll32 opens a document or URL on the owner's screen", c.raw)
	}
}

func hStart(a *analyzer, c *call) {
	if c.sh == ShellPowerShell {
		hStartProcess(a, c)
		return
	}
	for _, x := range c.args[1:] {
		l := strings.ToLower(x.s)
		if l == "/b" {
			return // the same console, no window
		}
		if !strings.HasPrefix(l, "/") {
			break
		}
	}
	a.add(RuleDesktop, "start opens a new window on the owner's screen", c.raw)
}

var programExt = regexp.MustCompile(`(?i)\.(exe|com|bat|cmd)$`)

func hStartProcess(a *analyzer, c *call) {
	opts, pos := positional(c, false, merge(psValue, map[string]bool{"-verb": true, "-argumentlist": true,
		"-workingdirectory": true, "-windowstyle": true, "-redirectstandardoutput": true,
		"-redirectstandarderror": true, "-redirectstandardinput": true, "-args": true}))
	for i := 1; i < len(c.args)-1; i++ {
		if strings.EqualFold(c.args[i].s, "-verb") && strings.EqualFold(c.args[i+1].s, "runas") {
			a.add(RuleElevation, "Start-Process -Verb RunAs asks for administrator rights", c.raw)
			return
		}
	}
	hidden := hasOpt(opts, "-nonewwindow")
	for i := 1; i < len(c.args)-1; i++ {
		if strings.EqualFold(c.args[i].s, "-windowstyle") && strings.EqualFold(c.args[i+1].s, "hidden") {
			hidden = true
		}
	}
	target := ""
	if len(pos) > 0 {
		target = pos[0].s
	}
	if target == "" || !programExt.MatchString(target) && strings.ContainsAny(target, `.:/\`) || !hidden {
		a.add(RuleDesktop, "Start-Process opens a window or a document on the owner's screen", c.raw)
	}
}

var (
	psKillCall   = regexp.MustCompile(`(?i)\.\s*kill\b`)
	psKillMethod = regexp.MustCompile(`(?i)\(\s*(get-process|gps|ps)\b[^)]*\)\s*\.\s*kill\s*\(`)
	psIEX        = regexp.MustCompile(`(?i)\b(iex|invoke-expression)\b`)
	psDownload   = regexp.MustCompile(`(?i)downloadstring|downloadfile|invoke-webrequest|invoke-restmethod|\biwr\b|\birm\b|net\.webclient`)
	psDirDelete  = regexp.MustCompile(`(?i)\[(system\.)?io\.directory\]::delete\s*\([^)]*,\s*\$true`)
)
