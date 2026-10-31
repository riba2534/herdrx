// Package launchers validates the account-level program list shown in the
// workbench "+" menu.
//
// The official Herdr app keeps that list in ~/.config/herdr-app/programs.json
// and lets an agent edit it (herdr-app CHANGELOG 0.1.8; defaults/programs.json).
// herdrx keeps the custom entries with the account, because the browser and the
// remote host are not the same machine. Built-in commands are the argv from
// herdr-app defaults/programs.json; this package only checks the user's additions.
package launchers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

const (
	maxLaunchers    = 24
	maxNameRunes    = 32
	maxCommandRunes = 200
	maxDocument     = 8 << 10
)

// reservedIDs are the built-in menu entries. Custom rows cannot replace them.
var reservedIDs = map[string]struct{}{
	"shell": {}, "claude": {}, "codex": {}, "gemini": {}, "cursor": {}, "pi": {}, "grok": {}, "agy": {},
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Launcher is one custom row. Command is a single shell line typed into the
// new tab's login shell and submitted with Enter. It is never executed on the
// workbench host.
type Launcher struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// Document is the JSON an agent (or the editor) writes.
type Document struct {
	Launchers []Launcher `json:"launchers"`
}

// ValidationError is safe to show to the signed-in user.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// Parse checks a launcher document and returns its canonical JSON.
func Parse(raw []byte) (Document, []byte, error) {
	if len(raw) > maxDocument {
		return Document{}, nil, &ValidationError{Message: "启动项内容过长"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, nil, &ValidationError{Message: "启动项需要是 {\"launchers\":[...]} 这样的 JSON"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Document{}, nil, &ValidationError{Message: "启动项需要是一份 JSON，不能在后面再加内容"}
	}
	if document.Launchers == nil {
		document.Launchers = []Launcher{}
	}
	if len(document.Launchers) > maxLaunchers {
		return Document{}, nil, &ValidationError{Message: "自定义启动项最多 24 个"}
	}
	seen := map[string]struct{}{}
	for i := range document.Launchers {
		item := &document.Launchers[i]
		if err := normalize(item, seen); err != nil {
			return Document{}, nil, err
		}
	}
	canonical, err := json.Marshal(document)
	if err != nil || len(canonical) > maxDocument {
		return Document{}, nil, &ValidationError{Message: "启动项内容过长"}
	}
	return document, canonical, nil
}

func normalize(item *Launcher, seen map[string]struct{}) error {
	if !idPattern.MatchString(item.ID) {
		return &ValidationError{Message: "启动项 id 要用小写字母开头，只能含小写字母、数字和连字符"}
	}
	if _, reserved := reservedIDs[item.ID]; reserved {
		return &ValidationError{Message: "id “" + item.ID + "” 是内置启动项，请换一个"}
	}
	if _, dup := seen[item.ID]; dup {
		return &ValidationError{Message: "启动项 id “" + item.ID + "” 重复了"}
	}
	seen[item.ID] = struct{}{}
	name := trimSpace(item.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes || hasControl(name) {
		return &ValidationError{Message: "启动项名称要在 1 到 32 个字符之间，且不能含换行"}
	}
	command := trimSpace(item.Command)
	if command == "" || utf8.RuneCountInString(command) > maxCommandRunes || hasControl(command) {
		return &ValidationError{Message: "启动命令要是一行，1 到 200 个字符，不能含换行或控制字符"}
	}
	item.Name = name
	item.Command = command
	return nil
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end {
		r, size := utf8.DecodeRuneInString(value[start:])
		if r != ' ' && r != '\t' {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(value[:end])
		if r != ' ' && r != '\t' {
			break
		}
		end -= size
	}
	return value[start:end]
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
