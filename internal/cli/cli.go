// Package cli is the entry point for every mode of the tmx binary: the daemon, the client
// commands (over HTTP with the owner token), `tmx mcp` and `tmx app`.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/desktop"
)

const usage = `tmx: runs a tree of coding agents on this machine (head → project leads → workers).

Daemon:
  tmx daemon                      run the daemon in the foreground
  tmx up                          start the daemon if needed; prints {"ready", "url"}
  tmx down                        stop the daemon (agents resume on the next start)
  tmx rollback                    restore previous binary and database (daemon must be stopped)
  tmx release keygen [--id ID]    the fleet release key (release machine only; prints the public key)
  tmx release sign TAG [--dry-run] check the release gates through gh, then sign the draft's SHA256SUMS
  tmx release sign-file SUMS --key PATH --out SIG --tag TAG  sign reviewed checksums offline; no upload
  tmx release sign-extra TAG [--dry-run] sign the public mirror's voice-engine release (whisper-engine-vX.Y.Z) after its gates
  tmx peers import-python         move this machine's Python tmx pair tokens into this daemon (fingerprints only)
  tmx status                      is the daemon running?
  tmx open                        print the browser URL (with the owner token)
  tmx app                         open the desktop app (starts the daemon if needed)

Fleet:
  tmx ls                          head → projects → agents with state and model
  tmx project import PROJECT --file FILE
  tmx project new NAME [--folder DIR] [--brief TEXT] [--harness claude|codex] [--model M]
  tmx project ls
  tmx project rm NAME --yes       stops and removes its agents; the folder stays
  tmx run NAME --project P [--profile P | --level worker|lead] [--model M] [--mode chat|terminal]
                           [--harness claude|codex|opencode|agy|pi|shell] [--prompt TEXT] [--role R] [--brief B]
                           [--no-wait] [--force]
  tmx send NAME [-f FILE]... TEXT...  message an agent as the owner (-f attaches a file)
  tmx start|stop|restart NAME
  tmx mode NAME [chat|terminal] [--start]   show or switch: chat, or the harness's own screen
                                  (Claude Code, Codex) on the same conversation; mid-turn it
                                  switches when the turn ends
  tmx rm NAME                     stop and remove an agent (and the agents below it)
  tmx approvals                   what is waiting for you
  tmx approve ID [ANSWER...]      allow a permission request, or answer a question
  tmx deny ID [--message TEXT]
  tmx harnesses                   which harnesses are installed, their modes
  tmx discover [--all]            claude/codex sessions running outside tmx, with their conversation
  tmx adopt PID --yes [--name N] [--project P] [--level L] [--conversation ID]
                                  stop that session (SIGTERM) and resume it under tmx

AI accounts (Claude, Codex, Antigravity; sign in from the app's Accounts page):
  tmx accounts [--json]           which accounts are signed in, and how much of each plan's limits
                                  is used, as the harnesses report it
  tmx accounts refresh [HARNESS]  check again now and wait for the answer

Resources:
  tmx resources | top             machine CPU, RAM, disk; each agent's CPU, memory, writes today
  tmx start NAME [--force] [--no-wait]   over the limits a start waits in the queue;
                                  --force starts anyway, --no-wait refuses instead
  tmx resume NAME                 continue an agent a brake paused
  tmx exempt NAME [--hours H]     no brakes for this agent for H hours (0 lifts it)
  tmx slot [--kind heavy|browser] -- CMD...   run CMD once a machine-wide slot is free
  tmx slots                       who holds and who waits for slots
  tmx processes list AGENT | system    owned processes or System tools (JSON)
  tmx processes start AGENT PURPOSE COMMAND [ARG...]   start an owned command
  tmx processes stop ID | promote ID PROJECT OWNER    stop or transfer a tool

Safety:
  tmx guard [status|on|off] [--for LEVEL]   the dangerous-command guard (default on)
  tmx gate [status|on|off]        the owner gate: Touch ID (macOS) or Windows Hello for sensitive actions
  tmx gate grace off|1h|24h|...   no prompt for that long after a confirmation (default 1h); tmx gate lock ends it

Chats with other people's tmx (also in the app):
  tmx chat ls                     chats: kind, name, status, unread, members, the agent taking part
  tmx chat read CHAT [--limit N]  recent messages, oldest first (CHAT = ID or name)
  tmx chat send CHAT [-f FILE]... TEXT...   write as the owner (-f attaches a file; @Name mentions)
  tmx chat join LINK              join a tmx://join/... invite (a DM or a group)
  tmx chat invite [CHAT] [--hours N]   print an invite link: a DM, or to the group CHAT
  tmx chat new NAME | members CHAT | leave CHAT [--yes]
  tmx chat agent CHAT AGENT|none  which local agent takes part (default: the head)
  tmx chat admin CHAT PERSON [--leave]   hand a group to PERSON (confirm with Touch ID / Windows Hello)
  tmx chat admin CHAT --cancel    withdraw that offer while it waits

Peer-to-peer (agents use phone approval):
  tmx p2p invite-create --label NAME [--kind person|group] [--ttl 24h] [--max-uses 1]
  tmx p2p agent-join --link LINK  preview and request phone approval
  tmx p2p invite-create|agent-join --approval ID  resume the same request

Peer-to-peer (network diagnostics):
  tmx p2p [status]                peer ID, addresses, reachability (direct/relayed/offline)
  tmx p2p invite [--group ID]     print an invite link (tmx://join/...)
  tmx p2p join LINK               pair with the inviter
  tmx p2p name [NEW]              show or change the name people see in chats
  tmx update [check|now|status|auto on|off|channel stable|latest|token T]
                                  updates from GitHub releases (DESIGN.md "Updates")
  tmx p2p peers | invites | revoke ID | send PEER [TEXT] | log

Recordings (of your own screen and microphone; they start only from the window):
  tmx rec ls [--limit N]          newest first: kind, state, length, size, segments
  tmx rec show ID                 details, with every segment's media file and 16 kHz WAV
  tmx rec rm ID --yes             delete a recording and its files

WhatsApp (your own account, linked like WhatsApp Web; nothing is sent without you):
  tmx wa [status]                 linked or not, the number, history sync, settings
  tmx wa link [--phone +41…]      prints a QR code to scan (or a pairing code to type on the phone)
  tmx wa unlink --yes             log tmx out of WhatsApp and forget its chats in tmx
  tmx wa chats [SEARCH...]        chats, newest activity first: unread, last message
  tmx wa read CHAT [--limit N]    the latest messages (CHAT: a name, a number or the JID)
  tmx wa send CHAT [-f FILE]... TEXT...   write as yourself (-f attaches a file)

Companion (the owner's desktop agent app):
  tmx companion status            what is installed and connected; checks Companion (read-only)
  tmx companion install|uninstall add/remove tmx's MCP entry and skill in Companion
  tmx companion connect           Companion asks you to confirm, then gives tmx its key (one click)
                                  (--token KEY or --token-stdin [--json]: a key you have)
  tmx companion token --rotate | disconnect | tasks | cancel ID
  tmx companion message [--reply-to ID] TEXT   write to Companion (the owner's conversation with it)

Voice (local transcription with whisper.cpp; nothing leaves the machine):
  tmx voice [status|setup]        engine and models; setup downloads what is missing
  tmx voice get|rm|model MODEL    download / remove / use a model (base-q5_1, small-q5_1, large-v3-turbo-q5_0)
  tmx voice language auto|CODE · tmx voice auto on|off   spoken language · transcribe new recordings by themselves
  tmx transcribe FILE [--language L] [--timestamps] [--json]   WAV, MP3, FLAC or Ogg: prints the text

Other:
  tmx mcp                         the MCP server agents run (started by the daemon)
  tmx mcp --companion             Companion's read-only link to tmx (started by Companion)
  tmx hook pretooluse             the guard's Claude Code hook (started by claude)
  tmx skill [install [--dir DIR] | path]   SKILL.md for AI agents: setup, chats, CLI
                                  (install: ~/.claude/skills/tmx; the daemon serves it at /skill.md)
  tmx version | help
`

// Main runs the command in args and returns the process exit code.
func Main(args []string) int { return runIO(args, os.Stdin, os.Stdout, os.Stderr) }

type cmdFunc func(e *env, args []string) error

var commands map[string]cmdFunc

func init() {
	commands = map[string]cmdFunc{
		"daemon": cmdDaemon, "up": cmdUp, "down": cmdDown, "status": cmdStatus, "open": cmdOpen,
		"rollback": cmdRollback,
		"peers":    cmdPeers,
		"app":      cmdApp, "ls": cmdLs, "project": cmdProject, "run": cmdRun, "send": cmdSend,
		"start": cmdAction("start"), "stop": cmdAction("stop"), "restart": cmdAction("restart"),
		"rm": cmdRm, "approvals": cmdApprovals, "approve": cmdApprove, "deny": cmdDeny,
		"mcp": cmdMCPArgs, "companion": cmdCompanion, "version": cmdVersion, "hook": cmdHook, "gate": cmdGate, "guard": cmdGuard,
		"harnesses": cmdHarnesses, "discover": cmdDiscover, "adopt": cmdAdopt,
		"p2p": cmdP2P, "chat": cmdChat, "skill": cmdSkill, "update": cmdUpdate,
		"mode":      cmdMode, // mode.go
		"processes": cmdProcesses,
		"rec":       cmdRec,      // rec.go
		"accounts":  cmdAccounts, // accounts.go
		"release":   cmdRelease,  // release.go
		"host":      cmdHost,     // host.go: a terminal agent's session host
	}
	for name, fn := range map[string]cmdFunc{ // resource control (resources.go, slot.go)
		"start": cmdStart, "resume": cmdResume, "exempt": cmdExempt, "resources": cmdResources,
		"top": cmdResources, "slot": cmdSlot, "slots": cmdSlots,
	} {
		commands[name] = fn
	}
	commands["voice"], commands["transcribe"] = cmdVoice, cmdTranscribe // voice.go
	commands["wa"], commands["whatsapp"] = cmdWa, cmdWa                 // wa.go
}

// run is runIO with nothing on stdin (tests).
func run(args []string, stdout, stderr io.Writer) int {
	return runIO(args, strings.NewReader(""), stdout, stderr)
}

func runIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args = launchArgs(args, desktop.Available)
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	fn, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "tmx: unknown command %q (tmx help lists them)\n", args[0])
		return 2
	}
	e := &env{in: stdin, out: stdout, errw: stderr}
	if err := fn(e, args[1:]); err != nil {
		var ex exitError
		if errors.As(err, &ex) {
			if ex.msg != "" {
				fmt.Fprintln(stderr, "tmx: "+ex.msg)
			}
			return ex.code
		}
		var ae *client.APIError
		if errors.As(err, &ae) {
			fmt.Fprintln(stderr, "tmx: "+ae.Msg)
			return 1
		}
		fmt.Fprintln(stderr, "tmx: "+err.Error())
		return 1
	}
	return 0
}

// exitError ends a command with a specific code (msg may be empty).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return exitError{code: 2, msg: fmt.Sprintf(format, args...)}
}

// launchArgs: the desktop app opened from Finder/Dock gets no arguments (or macOS's own
// -psn_ process serial number); that means "open the window", not "print the help".
func launchArgs(args []string, hasDesktop bool) []string {
	rest := args[:0:0]
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 && hasDesktop {
		return []string{"app"}
	}
	return rest
}
