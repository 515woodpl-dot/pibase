package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
)

func TestM6TOTPFlowAndAAL(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{6}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-m6-mfa", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	t.Setenv("PIBASE_AUTH_EXPOSE_TOKENS", "true")
	t.Setenv("PIBASE_ENV", "test")
	server, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	email := "m6-" + p.ID + "@example.test"
	signup := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var created struct {
		Confirmation string `json:"confirmation_token"`
	}
	if err := json.Unmarshal(signup, &created); err != nil || created.Confirmation == "" {
		t.Fatalf("signup response: %s", signup)
	}
	requestAuthJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/verify?token="+url.QueryEscape(created.Confirmation)+"&type=signup", p.AnonKey, "", http.StatusOK)
	login := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var aal1 struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(login, &aal1); err != nil || aal1.Access == "" || aal1.Refresh == "" {
		t.Fatalf("login response: %s", login)
	}
	jwtSecret := gatewayJWTSecret(t, adminURL, p.ID, masterKey)
	if claims, err := keys.VerifyJWT(aal1.Access, jwtSecret, time.Now()); err != nil || claims.AAL != "aal1" {
		t.Fatalf("initial AAL claims=%+v err=%v", claims, err)
	}

	enrolled := requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors", p.AnonKey, aal1.Access, `{"factor_type":"totp","friendly_name":"phone"}`, http.StatusOK)
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors", p.AnonKey, aal1.Access, `{"factor_type":"totp"}`, http.StatusOK)
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors", p.AnonKey, aal1.Access, `{"factor_type":"totp"}`, http.StatusUnprocessableEntity)
	var factor struct {
		ID   string `json:"id"`
		TOTP struct {
			Secret string `json:"secret"`
			URI    string `json:"uri"`
			QR     string `json:"qr_code"`
		} `json:"totp"`
	}
	if err := json.Unmarshal(enrolled, &factor); err != nil || factor.ID == "" || factor.TOTP.Secret == "" || !strings.HasPrefix(factor.TOTP.URI, "otpauth://totp/") || !strings.HasPrefix(factor.TOTP.QR, `<svg xmlns="http://www.w3.org/2000/svg"`) {
		t.Fatalf("enroll response: %s", enrolled)
	}
	user := requestAuthJSONBearer(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/user", p.AnonKey, aal1.Access, "", http.StatusOK)
	if !bytes.Contains(user, []byte(`"factor_type":"totp"`)) || !bytes.Contains(user, []byte(`"status":"unverified"`)) {
		t.Fatalf("user factors response: %s", user)
	}

	challenge := requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors/"+factor.ID+"/challenge", p.AnonKey, aal1.Access, `{}`, http.StatusOK)
	var challenged struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(challenge, &challenged); err != nil || challenged.ID == "" {
		t.Fatalf("challenge response: %s", challenge)
	}
	code, err := totpCode(factor.TOTP.Secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	verified := requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors/"+factor.ID+"/verify", p.AnonKey, aal1.Access, `{"challenge_id":"`+challenged.ID+`","code":"`+code+`"}`, http.StatusOK)
	var aal2 struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(verified, &aal2); err != nil || aal2.Access == "" || aal2.Refresh == "" {
		t.Fatalf("verify response: %s", verified)
	}
	if claims, err := keys.VerifyJWT(aal2.Access, jwtSecret, time.Now()); err != nil || claims.AAL != "aal2" || !bytes.Contains(claims.JSON, []byte(`"method":"totp"`)) {
		t.Fatalf("verified AAL claims=%s err=%v", claims.JSON, err)
	}
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/factors/"+factor.ID+"/verify", p.AnonKey, aal1.Access, `{"challenge_id":"`+challenged.ID+`","code":"`+code+`"}`, http.StatusBadRequest)

	rotated := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+aal2.Refresh+`"}`, http.StatusOK)
	var refreshed struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rotated, &refreshed); err != nil {
		t.Fatal(err)
	}
	if claims, err := keys.VerifyJWT(refreshed.Access, jwtSecret, time.Now()); err != nil || claims.AAL != "aal2" {
		t.Fatalf("refreshed AAL claims=%+v err=%v", claims, err)
	}
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/auth/v1/factors/"+factor.ID, p.AnonKey, aal1.Access, "", http.StatusUnprocessableEntity)
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/auth/v1/factors/"+factor.ID, p.AnonKey, refreshed.Access, "", http.StatusOK)
	downgraded := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+refreshed.Refresh+`"}`, http.StatusOK)
	if err := json.Unmarshal(downgraded, &refreshed); err != nil {
		t.Fatal(err)
	}
	if claims, err := keys.VerifyJWT(refreshed.Access, jwtSecret, time.Now()); err != nil || claims.AAL != "aal1" {
		t.Fatalf("post-unenroll AAL claims=%+v err=%v", claims, err)
	}
	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+aal1.Refresh+`"}`, http.StatusBadRequest)
}
