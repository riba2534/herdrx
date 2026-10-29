//go:build !unix

package agentlog

import (
	"errors"
	"os"
)

// 没有可核对的 fd 表时拒绝读取 agy 会话库（fail-closed）。
func agyOpenFDs() (map[string]agyFD, error) {
	return nil, errors.New("fd table unavailable")
}

func agyFileID(os.FileInfo) agyFD { return agyFD{} }
