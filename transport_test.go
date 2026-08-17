package meshroute

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestHMACSignature(t *testing.T) {
	body := []byte(`{"node":"a"}`)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, _ := http.NewRequest(http.MethodPost, "https://peer"+syncPath, bytes.NewReader(body))
	req.Header.Set("X-Mesh-Time", stamp)
	req.Header.Set("X-Mesh-Signature", signature("secret", req.Method, req.URL.Path, stamp, body))
	if !verifySignature("secret", req, body) {
		t.Fatal("valid signature rejected")
	}
	body[0] = 'x'
	if verifySignature("secret", req, body) {
		t.Fatal("modified body accepted")
	}
}
