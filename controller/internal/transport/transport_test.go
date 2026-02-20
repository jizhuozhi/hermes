package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHMACSigning_SetsHeaders(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := &HMACSigning{
		AK:     "test-ak",
		SK:     "test-sk",
		Region: "test-ns",
		Base:   http.DefaultTransport,
	}
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest("GET", server.URL+"/api/v1/config", nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Contains(t, capturedHeaders.Get("Authorization"), "HMAC-SHA256 Credential=test-ak")
	assert.Contains(t, capturedHeaders.Get("Authorization"), "Signature=")
	assert.NotEmpty(t, capturedHeaders.Get("X-Hermes-Timestamp"))
	assert.NotEmpty(t, capturedHeaders.Get("X-Hermes-Body-SHA256"))
	assert.Equal(t, "test-ns", capturedHeaders.Get("X-Hermes-Region"))
}

func TestHMACSigning_WithBody(t *testing.T) {
	var capturedBody string
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := &HMACSigning{
		AK:   "ak",
		SK:   "sk",
		Base: http.DefaultTransport,
	}
	client := &http.Client{Transport: rt}

	body := `{"name":"test"}`
	req, _ := http.NewRequest("PUT", server.URL+"/api/v1/domains", strings.NewReader(body))
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, body, capturedBody)
	assert.NotEmpty(t, capturedHeaders.Get("X-Hermes-Body-SHA256"))
}

func TestHMACSigning_DifferentSignaturesForDifferentPaths(t *testing.T) {
	var sigs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigs = append(sigs, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := &HMACSigning{AK: "ak", SK: "sk", Base: http.DefaultTransport}
	client := &http.Client{Transport: rt}

	req1, _ := http.NewRequest("GET", server.URL+"/path1", nil)
	resp1, _ := client.Do(req1)
	resp1.Body.Close()

	req2, _ := http.NewRequest("GET", server.URL+"/path2", nil)
	resp2, _ := client.Do(req2)
	resp2.Body.Close()

	require.Len(t, sigs, 2)
	// Signatures should be different for different paths
	// (they may be the same only if timestamp is identical and path differs, but the hash includes the path)
	assert.NotEqual(t, sigs[0], sigs[1])
}

func TestRegionOnly_SetsHeader(t *testing.T) {
	var capturedNS string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNS = r.Header.Get("X-Hermes-Region")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := &RegionOnly{Region: "production", Base: http.DefaultTransport}
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest("GET", server.URL+"/api/v1/config", nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "production", capturedNS)
}

func TestRegionOnly_DoesNotSetAuth(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := &RegionOnly{Region: "test", Base: http.DefaultTransport}
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest("GET", server.URL+"/test", nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Empty(t, capturedAuth, "RegionOnly should not set Authorization")
}
