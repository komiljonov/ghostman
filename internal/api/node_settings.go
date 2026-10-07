package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/komiljonov/ghostman/internal/db"
)

// Cascading per-node settings (follow_redirects now; auth, proxy later) are
// stored on folders and requests as plain values and resolved by the client,
// never by the server. Each is an optional field on the node's PATCH: absent
// keeps the stored value, and the column is NOT NULL, so there is no "clear".

// validateToggleSetting checks an optional inherit|global|on|off setting.
// nil is "not part of this update" and passes.
func validateToggleSetting(field string, value *string) error {
	if value == nil || slices.Contains(db.ToggleSettingValues, *value) {
		return nil
	}

	return fmt.Errorf("%s must be one of %s", field, strings.Join(db.ToggleSettingValues, ", "))
}
