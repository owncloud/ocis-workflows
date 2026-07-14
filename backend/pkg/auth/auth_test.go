package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc123": "abc123",
		"bearer abc123": "", // case-sensitive per RFC 6750
		"":              "",
		"Basic abc123":  "",
	}
	for header, want := range cases {
		if got := bearerToken(header); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	v := NewValidator("https://ocis.example", "https://ocis.example", false)

	if !v.OriginAllowed("") {
		t.Error("empty Origin (non-browser client) should be allowed")
	}
	if !v.OriginAllowed("https://ocis.example") {
		t.Error("matching origin should be allowed")
	}
	if v.OriginAllowed("https://evil.example") {
		t.Error("mismatched origin should not be allowed")
	}
}

func TestValidate(t *testing.T) {
	var idpURL string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = w.Write([]byte(`{"userinfo_endpoint":"` + idpURL + `/userinfo"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer idp.Close()
	idpURL = idp.URL

	v := NewValidator(idp.URL, idp.URL, false)

	if err := v.Validate(t.Context(), "good-token"); err != nil {
		t.Fatalf("Validate(good-token) = %v, want nil", err)
	}
	if err := v.Validate(t.Context(), "bad-token"); err == nil {
		t.Fatal("Validate(bad-token) = nil, want error")
	}
}
