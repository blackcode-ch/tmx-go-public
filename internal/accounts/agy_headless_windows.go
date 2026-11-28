//go:build windows

package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"golang.org/x/sys/windows"
)

const quotaPipe = "TMXG_AGY_QUOTA_PIPE"
const quotaProof = "TMXG_AGY_QUOTA_PROOF"
const quotaHelper = "TMXG_AGY_QUOTA_HELPER"
const quotaTarget = "TMXG_AGY_QUOTA_TARGET"

type quotaResult struct {
	Stdout []byte
	Stderr []byte
	Code   int
	Failed bool
}

// runHeadlessAgyQuota keeps the console attachment separate from the stdio
// transport. The conhost streams carry console control traffic; only the
// authenticated, single-use local connection carries the child result.
func (s *scope) runHeadlessAgyQuota(ctx context.Context, path string) ran {
	ctx, cancel := context.WithTimeout(ctx, s.o.CheckTimeout)
	defer cancel()
	failure := func(err error) ran {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			err = context.DeadlineExceeded
		}
		return ran{exitCode: -1, err: err}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return failure(err)
	}
	defer listener.Close()
	stopListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopListener()
	exe, err := os.Executable()
	if err != nil {
		return failure(err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return failure(err)
	}
	proof := rand.Text()
	powershell := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	// A GUI-subsystem helper cannot inherit the pseudoconsole directly.
	// Keep a console process alive as its parent so it can attach explicitly.
	const carrier = `$ErrorActionPreference='Stop'; Start-Process -FilePath $env:TMXG_AGY_QUOTA_HELPER -ArgumentList 'agy-quota-child' -NoNewWindow -Wait`
	cmd := hiddenexec.CommandContext(ctx, filepath.Join(system, "conhost.exe"),
		"--headless", powershell, "-NoProfile", "-NonInteractive", "-Command", carrier)
	cmd.SysProcAttr = hiddenexec.InheritConsoleAttributes(cmd.SysProcAttr)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = s.workDir()
	cmd.Env = s.env(append(agyUsageEnv(), quotaPipe+"="+listener.Addr().String(), quotaProof+"="+proof, quotaHelper+"="+exe, quotaTarget+"="+path)...)
	cmd.Cancel = func() error { return sysinfo.KillStarted(cmd.Process) }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failure(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		return failure(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = listener.Close()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	conn, err := listener.Accept()
	if err != nil {
		return failure(errors.New("headless quota helper did not connect"))
	}
	defer conn.Close()
	_ = listener.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	readProof := make([]byte, len(proof))
	if _, err := io.ReadFull(conn, readProof); err != nil {
		return failure(err)
	}
	if subtle.ConstantTimeCompare(readProof, []byte(proof)) != 1 {
		return failure(errors.New("invalid quota helper proof"))
	}
	if _, err := io.WriteString(conn, "1"); err != nil {
		return failure(err)
	}
	var reply quotaResult
	if err := json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&reply); err != nil {
		return failure(err)
	}
	if reply.Failed {
		return failure(errors.New("headless quota child could not start"))
	}
	return ran{out: reply.Stdout, errText: string(reply.Stderr), exitCode: reply.Code}
}

// RunAgyQuotaChild is the one-shot internal launcher invoked by headless conhost.
func RunAgyQuotaChild() error {
	path := os.Getenv(quotaTarget)
	if !filepath.IsAbs(path) {
		return errors.New("invalid Antigravity quota child command")
	}
	addr, proof := os.Getenv(quotaPipe), os.Getenv(quotaProof)
	if !strings.HasPrefix(addr, "127.0.0.1:") || proof == "" {
		return errors.New("missing local quota helper channel")
	}
	if cp, _ := windows.GetConsoleCP(); cp == 0 {
		const attachParentProcess = ^uint32(0)
		attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
		if ok, _, err := attach.Call(uintptr(attachParentProcess)); ok == 0 {
			return err
		}
	}
	conn, err := net.DialTimeout("tcp4", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, proof); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != '1' {
		return errors.New("quota helper was not acknowledged")
	}
	cmd := hiddenexec.CommandInheritConsole(path, "-p", "/usage", "--output-format", "json")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, quotaPipe) && !strings.EqualFold(key, quotaProof) &&
			!strings.EqualFold(key, quotaHelper) && !strings.EqualFold(key, quotaTarget) {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	out, stderr := &capped{max: 1 << 20}, &capped{max: 8 << 10, tail: true}
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return json.NewEncoder(conn).Encode(quotaResult{Code: -1, Failed: true})
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	disconnected := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(disconnected)
	}()
	select {
	case <-disconnected:
		_ = sysinfo.KillStarted(cmd.Process)
		<-exited
		return errors.New("quota probe canceled")
	case err := <-exited:
		result := quotaResult{Stdout: out.Bytes(), Stderr: stderr.Bytes(), Code: cmd.ProcessState.ExitCode()}
		var exitErr *exec.ExitError
		result.Failed = err != nil && !errors.As(err, &exitErr)
		return json.NewEncoder(conn).Encode(result)
	}
}
