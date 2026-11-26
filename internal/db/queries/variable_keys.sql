-- name: ListProjectVariableSources :many
-- Every text that may reference a variable, from the project's requests and
-- folders in one pass; the caller extracts the keys. Folders have no url,
-- rows or body, so those come back empty for them. Auth fields are read for
-- every type, since fields of an inactive type are kept for switching back.
SELECT url,
       headers,
       query_params,
       body,
       auth_bearer_token,
       auth_basic_username,
       auth_basic_password,
       auth_api_key_name,
       auth_api_key_value,
       auth_jwt_secret,
       auth_jwt_payload,
       auth_jwt_name,
       auth_jwt_prefix,
       pre_script,
       post_script
FROM requests
WHERE requests.project_id = @project_id

UNION ALL

SELECT '',
       '[]'::jsonb,
       '[]'::jsonb,
       '{"type": "none"}'::jsonb,
       auth_bearer_token,
       auth_basic_username,
       auth_basic_password,
       auth_api_key_name,
       auth_api_key_value,
       auth_jwt_secret,
       auth_jwt_payload,
       auth_jwt_name,
       auth_jwt_prefix,
       pre_script,
       post_script
FROM folders
WHERE folders.project_id = @project_id;
