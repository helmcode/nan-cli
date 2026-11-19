package cli

import "strings"

// errorsIsUserAbort reports whether huh stopped because the person stopped it.
func errorsIsUserAbort(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "user aborted") ||
		strings.Contains(err.Error(), "aborted"))
}
