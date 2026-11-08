package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedRouteUsesOnlyOneAuthenticatedBearer(t *testing.T) {
	routes := tenantRoutes{}
	for _, tenant := range []string{"counsel", "finance"} {
		routes[sha256.Sum256([]byte(tenant))] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Selected", tenant)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	for _, tc := range []struct {
		values   []string
		selected string
	}{
		{[]string{"Bearer counsel"}, "counsel"},
		{[]string{"Bearer finance"}, "finance"},
		{[]string{"Bearer unknown"}, ""},
		{[]string{"Bearer counsel", "Bearer finance"}, ""},
		{[]string{"Bearer counsel, Bearer finance"}, ""},
		{nil, ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?tenant=finance", nil)
		r.Header.Set("X-Tenant", "finance")
		for _, v := range tc.values {
			r.Header.Add("Authorization", v)
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Header().Get("Selected") != tc.selected || (tc.selected == "" && w.Code != http.StatusUnauthorized) {
			t.Errorf("invalid route selection with %d authorization headers", len(tc.values))
		}
	}
}
