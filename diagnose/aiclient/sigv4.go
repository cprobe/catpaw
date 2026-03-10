package aiclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AWSCredentials holds the access key, secret key and optional session token.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// signV4 signs an HTTP request using AWS Signature Version 4.
// The request body must already be set (and readable). payloadHash is
// the hex-encoded SHA-256 of the request body.
func signV4(req *http.Request, payloadHash, region, service string, creds AWSCredentials, now time.Time) {
	datestamp := now.UTC().Format("20060102")
	amzdate := now.UTC().Format("20060102T150405Z")

	req.Header.Set("x-amz-date", amzdate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("x-amz-security-token", creds.SessionToken)
	}

	signedHeaders, canonicalHeaders := buildCanonicalHeaders(req)
	canonicalRequest := buildCanonicalRequest(req, signedHeaders, canonicalHeaders, payloadHash)

	scope := datestamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzdate + "\n" + scope + "\n" + hashSHA256([]byte(canonicalRequest))

	signingKey := deriveSigningKey(creds.SecretAccessKey, datestamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
}

func buildCanonicalHeaders(req *http.Request) (signedHeaders, canonicalHeaders string) {
	headers := make(map[string]string)
	var keys []string
	for k := range req.Header {
		lk := strings.ToLower(k)
		headers[lk] = strings.TrimSpace(req.Header.Get(k))
		keys = append(keys, lk)
	}
	lk := "host"
	if _, ok := headers[lk]; !ok {
		headers[lk] = req.Host
		keys = append(keys, lk)
	}
	sort.Strings(keys)

	var chBuf, shBuf strings.Builder
	for i, k := range keys {
		chBuf.WriteString(k)
		chBuf.WriteByte(':')
		chBuf.WriteString(headers[k])
		chBuf.WriteByte('\n')
		if i > 0 {
			shBuf.WriteByte(';')
		}
		shBuf.WriteString(k)
	}
	return shBuf.String(), chBuf.String()
}

func buildCanonicalRequest(req *http.Request, signedHeaders, canonicalHeaders, payloadHash string) string {
	uri := req.URL.Path
	if uri == "" {
		uri = "/"
	}
	return req.Method + "\n" +
		uri + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash
}

func deriveSigningKey(secret, datestamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(datestamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hashSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
