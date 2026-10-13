package api

import (
	"fmt"
	"unicode/utf8"
)

// Pre- and post-request scripts are JavaScript the client runs around a send.
// The server stores the text and nothing else: it never executes or parses
// it. Unlike auth and follow_redirects they accumulate rather than cascade:
// every folder's script runs for every request under it, outermost first,
// then the request's own.
//
// On requests they travel like headers and body (GET /requests/{id} only, not
// the list); on folders they are in the folder list, so the client has the
// whole chain at send time without extra calls.

// maxScriptLength matches the <table>_pre_script_length and
// <table>_post_script_length CHECK constraints, counted in characters.
const maxScriptLength = 32768

// validateScript checks an optional script field. nil is "not part of this
// update" and passes; "" is a real value that clears the script.
func validateScript(field string, value *string) error {
	if value != nil && utf8.RuneCountInString(*value) > maxScriptLength {
		return fmt.Errorf("%s must be at most %d characters", field, maxScriptLength)
	}
	return nil
}
