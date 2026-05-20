package core

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type MemoryLog struct {
	mu   sync.Mutex
	Logs []map[string]string
}

func (m *MemoryLog) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	line := string(p)
	line = strings.TrimSpace(line)
	if line == "" {
		return len(p), nil
	}

	agent := "System"
	detail := line

	// Parse [Agent] pattern
	if start := strings.Index(line, "["); start != -1 {
		if end := strings.Index(line[start:], "]"); end != -1 {
			agent = line[start+1 : start+end]
			// remove the timestamp and agent from detail for cleaner UI
			detail = strings.TrimSpace(line[start+end+1:])
		}
	}

	// Remap live scan sub-agents to "Scanner" so they appear in the UI Live Scanner Feed
	if agent == "SectorLeader" || agent == "Regime" || agent == "Signal" {
		agent = "Scanner"
	}
	// Pre-market research phases stay separate — they run at boot before market hours
	if agent == "Screener" || agent == "GoldRatio" || agent == "Research" || agent == "DailyCache" {
		agent = "Research"
	}

	action := "Info"
	if strings.Contains(detail, "✅") {
		action = "Success"
	} else if strings.Contains(detail, "❌") || strings.Contains(detail, "failed") || strings.Contains(detail, "error") {
		action = "Error"
	} else if strings.Contains(detail, "⚠️") {
		action = "Warning"
	} else if agent == "Scanner" {
        action = "Scan"
    } else if agent == "Execution" {
        action = "Trade"
    }

	m.Logs = append(m.Logs, map[string]string{
		"time":   time.Now().Format("15:04:05"),
		"agent":  agent,
		"action": action,
		"detail": detail,
	})

	if len(m.Logs) > 60 {
		m.Logs = m.Logs[1:]
	}

	return len(p), nil
}

func (m *MemoryLog) GetLogs() []map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid race conditions during JSON marshalling
	out := make([]map[string]string, len(m.Logs))
	copy(out, m.Logs)
	return out
}

var GlobalMemLog = &MemoryLog{}

func InitGlobalLogger() {
	mw := io.MultiWriter(os.Stdout, GlobalMemLog)
	log.SetOutput(mw)
}
