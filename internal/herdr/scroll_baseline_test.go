package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
)

// Frozen pre-optimization implementation, used only for latency comparisons.
func sendScrollPerGesture(ctx context.Context, endpoint Endpoint, paneID string, lines, column, row int) error {
	if !publicID.MatchString(paneID) || lines == 0 || lines < -100 || lines > 100 || column < 0 || row < 0 {
		return fmt.Errorf("invalid terminal scroll")
	}
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		return err
	}
	var rect *Rect
	for _, layout := range snapshot.Layouts {
		for _, pane := range layout.Panes {
			if pane.PaneID == paneID {
				value := pane.Rect
				rect = &value
			}
		}
	}
	if rect == nil || rect.Width < 10 || rect.Width > 1000 || rect.Height < 3 || rect.Height > 500 {
		return fmt.Errorf("terminal layout is unavailable")
	}
	process, err := endpoint.OpenTerminal(ctx, TerminalOpen{PaneID: paneID, Mode: "control", Cols: uint16(rect.Width), Rows: uint16(rect.Height)})
	if err != nil {
		return err
	}
	defer func() { _ = process.Close(); _ = process.Wait() }()
	stop := context.AfterFunc(ctx, func() { _ = process.Close() })
	defer stop()
	scanner := bufio.NewScanner(process.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	// Wait for acceptance. Writing immediately could silently lose a gesture
	// when a native controller already owns the terminal.
	for scanner.Scan() {
		var frame struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return fmt.Errorf("invalid Herdr terminal response")
		}
		if frame.Type == "terminal.closed" {
			return fmt.Errorf("terminal scroll unavailable: %s", frame.Reason)
		}
		if frame.Type != "terminal.frame" {
			continue
		}
		direction := "down"
		if lines < 0 {
			direction = "up"
			lines = -lines
		}
		encoder := json.NewEncoder(process.Stdin())
		for lines > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			step := min(lines, 3)
			if err := encoder.Encode(map[string]any{"type": "terminal.scroll", "direction": direction, "lines": step, "source": "wheel", "column": min(column, rect.Width-1), "row": min(row, rect.Height-1)}); err != nil {
				return fmt.Errorf("send terminal scroll: %w", err)
			}
			lines -= step
		}
		if err := encoder.Encode(map[string]any{"type": "terminal.release"}); err != nil {
			return err
		}
		_ = process.Stdin().Close()
		// Drain until release is processed; killing the observer immediately after
		// writing could race with its stdin reader and discard the gesture.
		for scanner.Scan() {
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return scanner.Err()
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("terminal scroll ended before the controller was ready")
}
