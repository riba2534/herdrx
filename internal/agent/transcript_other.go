//go:build !unix

package agent

import "errors"

// Non-Unix hosts have no openat + O_NOFOLLOW equivalent. Fail the command so
// the client can retry. Do not fall back to a shell read, and do not report a
// permanent capability gap from here.
func serveTranscript(string, []byte) (transcriptWireResponse, error) {
	return transcriptWireResponse{}, errors.New("structured transcript reader is unavailable on this platform")
}
