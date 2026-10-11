package control

import "github.com/jackc/pgx/v5"

// Auth administration helpers are private to the project's admin role. A
// customer SECURITY DEFINER RPC may call them after its own authorization
// checks, keeping user creation and application-row writes in one transaction.
func authAdminHelpersSQL(admin string) string {
	role := pgx.Identifier{admin}.Sanitize()
	return `
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;
CREATE OR REPLACE FUNCTION auth.notify_session_revocation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
  IF NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL AND NEW.replaced_by IS NULL THEN
    PERFORM pg_notify('pibase_sessions',NEW.user_id::text);
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION auth.notify_session_revocation() FROM PUBLIC;
DROP TRIGGER IF EXISTS pibase_session_revocation ON auth.refresh_tokens;
CREATE TRIGGER pibase_session_revocation AFTER UPDATE OF revoked_at ON auth.refresh_tokens
FOR EACH ROW EXECUTE FUNCTION auth.notify_session_revocation();

CREATE OR REPLACE FUNCTION auth.admin_create_user(p_username text, p_email text, p_password text, p_app_metadata jsonb DEFAULT '{}'::jsonb)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE new_id uuid := gen_random_uuid(); normalized_email text := NULLIF(lower(btrim(p_email)),''); normalized_username text := NULLIF(lower(btrim(p_username)),'');
BEGIN
  IF normalized_email IS NULL AND normalized_username IS NULL THEN RAISE EXCEPTION 'email or username is required'; END IF;
  IF normalized_username IS NOT NULL AND normalized_username !~ '^[a-z0-9][a-z0-9_.-]{2,63}$' THEN RAISE EXCEPTION 'invalid username'; END IF;
  IF octet_length(p_password) < 6 OR octet_length(p_password) > 72 THEN RAISE EXCEPTION 'password must be 6 to 72 bytes'; END IF;
  INSERT INTO auth.users(id,email,username,password_hash,email_confirmed_at,raw_app_meta_data)
  VALUES(new_id,normalized_email,normalized_username,public.crypt(p_password,public.gen_salt('bf',12)),now(),COALESCE(p_app_metadata,'{}'::jsonb));
  RETURN new_id;
END $$;
CREATE OR REPLACE FUNCTION auth.admin_end_sessions(p_user_id uuid, p_keep_session uuid DEFAULT NULL)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE keep_group uuid;
BEGIN
  IF p_keep_session IS NOT NULL THEN
    SELECT COALESCE(session_id,id) INTO keep_group FROM auth.refresh_tokens WHERE id=p_keep_session AND user_id=p_user_id;
    IF keep_group IS NULL THEN RAISE EXCEPTION 'session does not belong to user'; END IF;
  END IF;
  UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now())
  WHERE user_id=p_user_id AND (keep_group IS NULL OR COALESCE(session_id,id)<>keep_group);
END $$;
CREATE OR REPLACE FUNCTION auth.admin_set_password(p_user_id uuid, p_password text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE changed uuid;
BEGIN
  IF octet_length(p_password) < 6 OR octet_length(p_password) > 72 THEN RAISE EXCEPTION 'password must be 6 to 72 bytes'; END IF;
  UPDATE auth.users SET password_hash=public.crypt(p_password,public.gen_salt('bf',12)),updated_at=now()
  WHERE id=p_user_id RETURNING id INTO changed;
  IF changed IS NULL THEN RAISE EXCEPTION 'user not found'; END IF;
  PERFORM auth.admin_end_sessions(p_user_id);
END $$;
REVOKE ALL ON FUNCTION auth.admin_create_user(text,text,text,jsonb),auth.admin_end_sessions(uuid,uuid),auth.admin_set_password(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth.admin_create_user(text,text,text,jsonb),auth.admin_end_sessions(uuid,uuid),auth.admin_set_password(uuid,text) TO ` + role + `;
`
}
