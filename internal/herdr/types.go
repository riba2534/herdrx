package herdr

import (
	"context"
	"encoding/json"
	"io"
	"net"

	"github.com/riba2534/herdrx/internal/terminalgeometry"
)

type Workspace struct {
	ID          string             `json:"workspace_id"`
	Label       string             `json:"label"`
	Number      int                `json:"number"`
	ActiveTabID string             `json:"active_tab_id"`
	AgentStatus string             `json:"agent_status"`
	Focused     bool               `json:"focused"`
	PaneCount   int                `json:"pane_count"`
	TabCount    int                `json:"tab_count"`
	Branch      string             `json:"branch,omitempty"`
	Worktree    *WorkspaceWorktree `json:"worktree,omitempty"`
	Tokens      map[string]any     `json:"tokens,omitempty"`
}

type WorkspaceWorktree struct {
	RepoKey          string `json:"repo_key"`
	RepoName         string `json:"repo_name"`
	RepoRoot         string `json:"repo_root"`
	CheckoutPath     string `json:"checkout_path"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
}

type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

type Scroll struct {
	MaxOffsetFromBottom int `json:"max_offset_from_bottom"`
	OffsetFromBottom    int `json:"offset_from_bottom"`
	ViewportRows        int `json:"viewport_rows"`
}

type Pane struct {
	ID                    string  `json:"pane_id"`
	WorkspaceID           string  `json:"workspace_id"`
	TabID                 string  `json:"tab_id"`
	TerminalID            string  `json:"terminal_id"`
	Label                 string  `json:"label,omitempty"`
	TerminalTitle         string  `json:"terminal_title,omitempty"`
	TerminalTitleStripped string  `json:"terminal_title_stripped,omitempty"`
	Agent                 string  `json:"agent,omitempty"`
	AgentStatus           string  `json:"agent_status"`
	CWD                   string  `json:"cwd,omitempty"`
	ForegroundCWD         string  `json:"foreground_cwd,omitempty"`
	Focused               bool    `json:"focused"`
	Revision              int64   `json:"revision"`
	RightClickPassthrough bool    `json:"right_click_passthrough"`
	Scroll                *Scroll `json:"scroll,omitempty"`
}

type Agent struct {
	Name           string `json:"name,omitempty"`
	Agent          string `json:"agent"`
	AgentStatus    string `json:"agent_status"`
	PaneID         string `json:"pane_id"`
	WorkspaceID    string `json:"workspace_id"`
	TabID          string `json:"tab_id"`
	CWD            string `json:"cwd,omitempty"`
	Focused        bool   `json:"focused"`
	StateChangeSeq int64  `json:"state_change_seq,omitempty"`
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type LayoutPane struct {
	PaneID  string `json:"pane_id"`
	Focused bool   `json:"focused"`
	Rect    Rect   `json:"rect"`
}

type LayoutSplit struct {
	ID        string  `json:"id"`
	Direction string  `json:"direction"`
	Ratio     float64 `json:"ratio"`
	Rect      Rect    `json:"rect"`
}

type Layout struct {
	WorkspaceID   string        `json:"workspace_id"`
	TabID         string        `json:"tab_id"`
	FocusedPaneID string        `json:"focused_pane_id"`
	Area          Rect          `json:"area"`
	Panes         []LayoutPane  `json:"panes"`
	Splits        []LayoutSplit `json:"splits"`
	Zoomed        bool          `json:"zoomed"`
}

type Snapshot struct {
	Version            string      `json:"version"`
	Protocol           int         `json:"protocol"`
	FocusedWorkspaceID string      `json:"focused_workspace_id"`
	FocusedTabID       string      `json:"focused_tab_id"`
	FocusedPaneID      string      `json:"focused_pane_id"`
	Workspaces         []Workspace `json:"workspaces"`
	Tabs               []Tab       `json:"tabs"`
	Panes              []Pane      `json:"panes"`
	Layouts            []Layout    `json:"layouts"`
	Agents             []Agent     `json:"agents"`
}

type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *APIError       `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type TerminalProcess interface {
	Stdout() io.Reader
	Stdin() io.WriteCloser
	Wait() error
	Close() error
}

type Endpoint interface {
	Snapshot(context.Context) (Snapshot, error)
	Call(context.Context, string, any) (json.RawMessage, error)
	OpenTerminal(context.Context, TerminalOpen) (TerminalProcess, error)
	StageImage(context.Context, string, io.Reader) (string, error)
	Close() error
}

type TerminalOpen struct {
	PaneID   string
	Mode     string
	Takeover bool
	Cols     uint16
	Rows     uint16
}

type TerminalGeometry = terminalgeometry.Geometry

// NativeScrollEndpoint preserves the source PTY geometry during native wheel
// control. An endpoint without this capability cannot safely emulate it.
type NativeScrollEndpoint interface {
	TerminalGeometry(context.Context, string) (TerminalGeometry, error)
	OpenTerminalSocket(context.Context) (net.Conn, error)
}
