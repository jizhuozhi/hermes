package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HMACSigning is an http.RoundTripper that signs every outgoing
// request with HMAC-SHA256(SK, METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + BODY_HASH)
// and sets the X-Hermes-Namespace header for namespace-scoped API access.
type HMACSigning struct {
	AK        string
	SK        string
	Namespace string
	Base      http.RoundTripper
}

func (t *HMACSigning) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the caller's original.
	req2 := req.Clone(req.Context())

	ts := strconv.FormatInt(time.Now().Unix(), 10)

	var bodyHash string
	if req2.Body != nil && req2.Body != http.NoBody {
		bodyBytes, err := io.ReadAll(req2.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body for signing: %w", err)
		}
		req2.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
		h := sha256.Sum256(bodyBytes)
		bodyHash = hex.EncodeToString(h[:])
	} else {
		h := sha256.Sum256(nil)
		bodyHash = hex.EncodeToString(h[:])
	}

	stringToSign := req2.Method + "\n" + req2.URL.Path + "\n" + ts + "\n" + bodyHash

	mac := hmac.New(sha256.New, []byte(t.SK))
	mac.Write([]byte(stringToSign))
	sig := hex.EncodeToString(mac.Sum(nil))

	req2.Header.Set("Authorization", fmt.Sprintf("HMAC-SHA256 Credential=%s, Signature=%s", t.AK, sig))
	req2.Header.Set("X-Hermes-Timestamp", ts)
	req2.Header.Set("X-Hermes-Body-SHA256", bodyHash)
	if t.Namespace != "" {
		req2.Header.Set("X-Hermes-Namespace", t.Namespace)
	}

	return t.Base.RoundTrip(req2)
}

// NamespaceOnly sets the X-Hermes-Namespace header without HMAC signing.
type NamespaceOnly struct {
	Namespace string
	Base      http.RoundTripper
}

func (t *NamespaceOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the caller's original.
	req2 := req.Clone(req.Context())
	req2.Header.Set("X-Hermes-Namespace", t.Namespace)
	return t.Base.RoundTrip(req2)
}
