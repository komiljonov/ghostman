package db

// Values of a cascading on/off node setting such as follow_redirects. They
// mirror the <table>_follow_redirects CHECK constraints; changing one means
// changing the other in a migration.
//
// Resolving the effective value through the parent chain is the client's job;
// the server only stores what each node says.
const (
	// SettingInherit resolves through the parent chain, ending at the
	// client's global default. The default for every node.
	SettingInherit = "inherit"

	// SettingGlobal skips all ancestors and uses the client's global default.
	SettingGlobal = "global"

	// SettingOn and SettingOff are explicit, regardless of ancestors.
	SettingOn  = "on"
	SettingOff = "off"
)

// ToggleSettingValues lists every value, in the order error messages name
// them.
var ToggleSettingValues = []string{SettingInherit, SettingGlobal, SettingOn, SettingOff}
