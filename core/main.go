//go:build windows

// Command jellyfinrpc-core builds the Jellyfin RPC engine as a Windows DLL
// (jellyfinrpc_core.dll). The version.dll proxy next to it loads it inside
// Jellyfin Desktop, so Discord Rich Presence runs while Jellyfin Desktop runs
// and stops when it closes. Nothing else to start.
//
// Exports:
//
//	JfRpcStart()  starts the engine on a background goroutine (returns at once)
//	JfRpcStop()   stops it and clears the Discord presence
package main

import "C"

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Set with -ldflags "-X main.version=...".
var version = "dev"

var (
	mu      sync.Mutex
	cancel  context.CancelFunc
	logFile *os.File
)

//export JfRpcStart
func JfRpcStart() {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		return
	}
	// Jellyfin Desktop starts helper processes (QtWebEngineProcess) that load
	// the proxy too. Run only in the main process, and only one engine at a time.
	if skipHostProcess() || !singleInstance() {
		return
	}
	dir, err := configDir()
	if err != nil {
		return
	}
	openLog(dir)
	log.Printf("jellyfin-rpc %s starting in %s (pid %d)", version, exeName(), os.Getpid())

	ctx, c := context.WithCancel(context.Background())
	cancel = c
	go newEngine(dir).run(ctx)
}

//export JfRpcStop
func JfRpcStop() {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		log.Printf("jellyfin-rpc stopping")
		cancel()
		cancel = nil
	}
}

func configDir() (string, error) {
	base, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "jellyfin-rpc")
	return dir, os.MkdirAll(dir, 0o700)
}

func openLog(dir string) {
	path := filepath.Join(dir, "jellyfin-rpc.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	logFile = f
	log.SetOutput(io.Writer(f))
	log.SetFlags(log.LstdFlags)
}

func exeName() string {
	exe, err := os.Executable()
	if err != nil {
		return "?"
	}
	return filepath.Base(exe)
}

func skipHostProcess() bool {
	base := strings.ToLower(exeName())
	for _, h := range []string{"qtwebengineprocess", "crashpad", "jellyfin-rpc"} {
		if strings.Contains(base, h) {
			return true
		}
	}
	return false
}

// singleInstance holds a named mutex for the life of the process.
func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\jellyfin-rpc-engine`)
	_, err := windows.CreateMutex(nil, false, name)
	return err != windows.ERROR_ALREADY_EXISTS
}

func main() {}
