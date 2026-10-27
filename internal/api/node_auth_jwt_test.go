package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const samplePayload = `{"sub":"{{user_id}}","iat":{{$now}},"exp":{{$now+3600}},"scope":["read","write"]}`

func TestPatchAuthJWT(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-jwt")

	for _, target := range newAuthTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			want := defaultAuth

			// Full JWT config: stored as authored, including a payload that is
			// not valid JSON until the client resolves its {{vars}}.
			full := `{"type":"jwt","jwt_algorithm":"HS512","jwt_secret":"{{jwt_secret}}","jwt_secret_base64":true,` +
				`"jwt_payload":` + jsonString(t, samplePayload) + `,"jwt_add_to":"query","jwt_name":"access_token","jwt_prefix":""}`
			want.Type, want.JWTAlgorithm, want.JWTSecret, want.JWTSecretBase64 = "jwt", "HS512", "{{jwt_secret}}", true
			want.JWTPayload, want.JWTAddTo, want.JWTName = samplePayload, "query", "access_token"
			if got := f.patchAuth(t, owner.Token, target.patch, full); got != want {
				t.Errorf("full JWT: PATCH response = %+v, want %+v", got, want)
			}
			checkAuthEverywhere(t, target, want)

			// Partial merge inside the object: only the sent fields change.
			want.JWTAddTo, want.JWTName, want.JWTPrefix = "header", "", "Token "
			if got := f.patchAuth(t, owner.Token, target.patch, `{"type":"jwt","jwt_add_to":"header","jwt_name":"","jwt_prefix":"Token "}`); got != want {
				t.Errorf("partial JWT: = %+v, want %+v", got, want)
			}
			want.JWTSecretBase64 = false
			f.patchAuth(t, owner.Token, target.patch, `{"type":"jwt","jwt_secret_base64":false}`)
			checkAuthEverywhere(t, target, want)

			// Switching away keeps the JWT fields; switching back restores them.
			want.Type, want.BearerToken = "bearer", "{{token}}"
			f.patchAuth(t, owner.Token, target.patch, `{"type":"bearer","bearer_token":"{{token}}"}`)
			checkAuthEverywhere(t, target, want)

			want.Type = "jwt"
			f.patchAuth(t, owner.Token, target.patch, `{"type":"jwt"}`)
			checkAuthEverywhere(t, target, want)
			if want.JWTAlgorithm != "HS512" || want.JWTPayload != samplePayload || want.BearerToken != "{{token}}" {
				t.Fatalf("test bookkeeping: preserved fields lost: %+v", want)
			}

			// Every algorithm is accepted.
			for _, alg := range []string{"HS256", "HS384", "HS512"} {
				if got := f.patchAuth(t, owner.Token, target.patch, `{"type":"jwt","jwt_algorithm":"`+alg+`"}`); got.JWTAlgorithm != alg {
					t.Errorf("algorithm %s stored as %s", alg, got.JWTAlgorithm)
				}
			}
		})
	}
}

func TestPatchAuthJWTValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-jwt-validation")

	tests := []struct {
		name, auth, message string
	}{
		{"unknown algorithm", `{"type":"jwt","jwt_algorithm":"RS256"}`, "auth.jwt_algorithm must be one of HS256, HS384, HS512"},
		{"lowercase algorithm", `{"type":"jwt","jwt_algorithm":"hs256"}`, "auth.jwt_algorithm must be one of"},
		{"none algorithm", `{"type":"jwt","jwt_algorithm":"none"}`, "auth.jwt_algorithm must be one of"},
		{"unknown placement", `{"type":"jwt","jwt_add_to":"cookie"}`, "auth.jwt_add_to must be one of header, query"},
		{"payload over 8192", `{"type":"jwt","jwt_payload":` + jsonString(t, strings.Repeat("p", 8193)) + `}`, "auth.jwt_payload must be at most 8192 characters"},
		{"secret_base64 not a boolean", `{"type":"jwt","jwt_secret_base64":"true"}`, "auth.jwt_secret_base64 must be a boolean"},
		{"secret_base64 null", `{"type":"jwt","jwt_secret_base64":null}`, "auth.jwt_secret_base64 must be a boolean"},
		{"secret not a string", `{"type":"jwt","jwt_secret":42}`, "auth.jwt_secret must be a string"},
		{"unknown jwt field", `{"type":"jwt","jwt_expires_in":3600}`, "auth.jwt_expires_in is not a known field"},
		{"jwt fields without a type", `{"jwt_algorithm":"HS256"}`, "auth.type is required"},
	}

	for _, target := range newAuthTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			for _, tt := range tests {
				rec := do(t, f.handler, http.MethodPatch, target.patch, `{"auth":`+tt.auth+`}`, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want 400", tt.name, rec.Code)
					continue
				}
				if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
					t.Errorf("%s: message = %q, want prefix %q", tt.name, msg, tt.message)
				}
			}
			checkAuthEverywhere(t, target, defaultAuth)

			// The payload limit itself is accepted.
			longest := strings.Repeat("é", maxAuthValueLength)
			if got := f.patchAuth(t, owner.Token, target.patch, `{"type":"jwt","jwt_payload":`+jsonString(t, longest)+`}`); got.JWTPayload != longest {
				t.Error("an 8192-character payload was not stored whole")
			}
		})
	}
}

func TestAuthJWTAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)
	folder := f.createFolder(t, f.owner.Token, f.ownerProject.ID, "Hidden", nil)
	request := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})
	f.patchAuth(t, f.owner.Token, "/api/v1/requests/"+request.ID, `{"type":"jwt","jwt_secret":"{{s}}"}`)

	for _, token := range []string{f.limited.Token, f.outsider.Token} {
		for _, path := range []string{"/api/v1/folders/" + folder.ID, "/api/v1/requests/" + request.ID} {
			if rec := do(t, f.handler, http.MethodPatch, path, `{"auth":{"type":"jwt","jwt_secret":"stolen"}}`, token); rec.Code != http.StatusNotFound {
				t.Errorf("PATCH %s: status = %d, want 404", path, rec.Code)
			}
		}
	}

	if got := f.getRequestDetail(t, f.owner.Token, request.ID).Auth; got.JWTSecret != "{{s}}" {
		t.Errorf("hidden request jwt_secret = %q after denied patches", got.JWTSecret)
	}
}

func TestAuthJWTCheckConstraints(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-jwt-check")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	for table, id := range map[string]string{"folders": folder.ID, "requests": request.ID} {
		for set, constraint := range map[string]string{
			"auth_jwt_algorithm = 'RS256'":         table + "_auth_jwt_algorithm",
			"auth_jwt_add_to = 'cookie'":           table + "_auth_jwt_add_to",
			"auth_jwt_payload = repeat('x', 8193)": table + "_auth_jwt_payload_length",
			"auth_type = 'oauth2'":                 table + "_auth_type",
		} {
			_, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET "+set+" WHERE id = $1", id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != constraint {
				t.Errorf("%s: SET %s: error = %v, want a CHECK violation of %s", table, set, err, constraint)
			}
		}

		// 'jwt' itself is allowed by the widened type CHECK.
		if _, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET auth_type = 'jwt' WHERE id = $1", id); err != nil {
			t.Errorf("%s: auth_type 'jwt' refused: %v", table, err)
		}
	}
}
