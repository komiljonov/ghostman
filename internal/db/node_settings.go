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

// Authorization types and API-key placements. They mirror the
// <table>_auth_type and <table>_auth_api_key_in CHECK constraints.
const (
	AuthInherit = "inherit"
	AuthNone    = "none"
	AuthBearer  = "bearer"
	AuthBasic   = "basic"
	AuthAPIKey  = "api_key"

	APIKeyInHeader = "header"
	APIKeyInQuery  = "query"
)

// AuthTypes and APIKeyPlacements list every value, in the order error
// messages name them.
var (
	AuthTypes        = []string{AuthInherit, AuthNone, AuthBearer, AuthBasic, AuthAPIKey}
	APIKeyPlacements = []string{APIKeyInHeader, APIKeyInQuery}
)
