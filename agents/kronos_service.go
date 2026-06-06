package agents

// kronos_service.go — manages the Kronos AI microservice lifecycle from within
// bnf_go_engine.exe. On startup the engine calls EnsureKronosRunning(), which:
//
//  1. Skips immediately if the Kronos HTTP service is already responding.
//  2. Locates start_kronos.ps1 relative to the executable / working directory.
//  3. Launches start_kronos.ps1 as a detached background process (no terminal).
//  4. Attempts to register the task in Windows Task Scheduler (requires the
//     engine to be running as Administrator — silently ignored if not).
//  5. Waits up to 30 s for the service to come online.
//
// The caller (main.go) simply does:
//
//	kronosClient := agents.NewKronosClient(kronosURL)
//	kronosClient.EnsureKronosRunning()

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	kronosScriptName   = "start_kronos.ps1"
	kronosRegisterName = "register_kronos_task.ps1"
	kronosTaskName     = "KronosAIService"
	kronosStartTimeout = 30 * time.Second
)

// EnsureKronosRunning checks whether the Kronos service is alive.
// If not, it locates start_kronos.ps1 and starts it, then optionally
// registers it as a Windows Scheduled Task for auto-start on next boot.
func (k *KronosClient) EnsureKronosRunning() {
	if k.IsAlive() {
		log.Println("[Kronos] Service already running ✅")
		return
	}

	scriptPath := findKronosScript(kronosScriptName)
	if scriptPath == "" {
		log.Println("[Kronos] start_kronos.ps1 not found — skipping auto-start")
		return
	}

	log.Printf("[Kronos] Starting service via %s ...", scriptPath)
	if err := launchKronosBackground(scriptPath); err != nil {
		log.Printf("[Kronos] Failed to launch service: %v", err)
		return
	}

	// Attempt Task Scheduler registration (silent if not Admin).
	go func() {
		// Give the service a moment to start before registering the task.
		time.Sleep(5 * time.Second)
		registerKronosTask(filepath.Dir(scriptPath))
	}()

	// Wait for the service to respond.
	deadline := time.Now().Add(kronosStartTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if k.IsAlive() {
			log.Println("[Kronos] Service online ✅")
			return
		}
	}
	log.Println("[Kronos] Service did not respond within 30 s — AI ranking will be skipped")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// findKronosScript searches for the named script in:
//  1. <exe-dir>/kronos/
//  2. <cwd>/kronos/
//  3. <exe-dir>/
//  4. <cwd>/
func findKronosScript(name string) string {
	candidates := buildSearchPaths(name)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func buildSearchPaths(name string) []string {
	var dirs []string

	// Directory of the running executable
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs, filepath.Join(exeDir, "kronos"), exeDir)
	}

	// Current working directory
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, "kronos"), cwd)
	}

	var paths []string
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, name))
	}
	return paths
}

// launchKronosBackground starts start_kronos.ps1 as a fully detached process
// (hidden console window, not a child of the engine process).
func launchKronosBackground(scriptPath string) error {
	cmd := exec.Command(
		"powershell.exe",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden",
		"-File", scriptPath,
	)
	cmd.Dir = filepath.Dir(scriptPath)

	// Detach stdout/stderr to a log file alongside the script.
	logPath := filepath.Join(filepath.Dir(scriptPath), "kronos_service.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	// SysProcAttr is set in kronos_service_windows.go (build-tagged) so this
	// file stays platform-agnostic.
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("powershell start: %w", err)
	}
	log.Printf("[Kronos] Launched PID %d", cmd.Process.Pid)

	// Release the child so it outlives the parent without becoming a zombie.
	cmd.Process.Release() //nolint:errcheck
	return nil
}

// registerKronosTask runs register_kronos_task.ps1 via PowerShell.
// This requires the engine to be running with Administrator privileges.
// If it fails (common case — normal user), the error is logged and ignored.
func registerKronosTask(kronosDir string) {
	// First check whether the task already exists (schtasks — available to all users).
	check := exec.Command("schtasks", "/Query", "/TN", kronosTaskName)
	if err := check.Run(); err == nil {
		log.Printf("[Kronos] Task Scheduler entry '%s' already registered", kronosTaskName)
		return
	}

	registerScript := filepath.Join(kronosDir, kronosRegisterName)
	if _, err := os.Stat(registerScript); err != nil {
		log.Printf("[Kronos] %s not found — skipping Task Scheduler registration", kronosRegisterName)
		return
	}

	log.Printf("[Kronos] Attempting Task Scheduler registration (needs Admin)...")
	cmd := exec.Command(
		"powershell.exe",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-File", registerScript,
	)
	cmd.Dir = kronosDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[Kronos] Task Scheduler registration failed (not Admin?): %v\n%s", err, out)
	} else {
		log.Printf("[Kronos] Task Scheduler registration OK:\n%s", out)
	}
}
