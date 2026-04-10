package mcpsrv

import (
	"time"

	"github.com/kai/freestone/internal/audit"
)

// auditLog records a write tool invocation. Called after the tool handler
// returns so that both success and failure are captured.
func auditLog(logger *audit.Logger, tool, provider string, inputs map[string]string, err error) {
	if logger == nil {
		return
	}
	rec := audit.Record{
		Time:     time.Now().UTC(),
		Tool:     tool,
		Provider: provider,
		Inputs:   inputs,
		Outcome:  "ok",
	}
	if err != nil {
		rec.Outcome = "error"
		rec.Error = err.Error()
	}
	// Best-effort: audit log write failures are not surfaced to the caller.
	logger.Log(rec)
}
