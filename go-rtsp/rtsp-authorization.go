package rtsp

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type authenticate interface {
	setUri(uri string)
	setMethod(method string)
	setUserInfo(userName, passwd string)
	setRealm(realm string)
	authenticateInfo() string
	// decode reads a WWW-Authenticate challenge and reports a challenge that
	// can not be answered.
	decode(challenge string) error
	check(string) bool
	wwwAuthenticate() string
}

func createAuthByAuthenticate(auth string) (authenticate, error) {
	auth = strings.TrimSpace(auth)
	if strings.HasPrefix(strings.ToLower(auth), "basic") {
		return &basicAuth{}, nil
	} else if strings.HasPrefix(strings.ToLower(auth), "digest") {
		return &digestAuth{}, nil
	}
	return nil, errors.New("unsupport Authorization scheme: " + auth)
}

type basicAuth struct {
	userName string
	passwd   string
	realm    string
}

func (basic *basicAuth) setUri(uri string)       {}
func (basic *basicAuth) setMethod(method string) {}
func (basic *basicAuth) setRealm(realm string)   { basic.realm = realm }

// decode accepts any basic challenge: the credentials do not depend on it.
func (basic *basicAuth) decode(challenge string) error { return nil }

func (basic *basicAuth) setUserInfo(userName, passwd string) {
	basic.userName = userName
	basic.passwd = passwd
}

func (basic *basicAuth) authenticateInfo() string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(basic.userName+":"+basic.passwd)))
}

func (basic *basicAuth) check(authorization string) bool {
	parts := strings.SplitN(strings.TrimSpace(authorization), " ", 2)
	if len(parts) < 2 || parts[0] != "Basic" {
		return false
	}
	response := strings.TrimSpace(parts[1])
	dst := base64.StdEncoding.EncodeToString([]byte(basic.userName + ":" + basic.passwd))
	return subtle.ConstantTimeCompare([]byte(response), []byte(dst)) == 1
}

func (basic *basicAuth) wwwAuthenticate() string {
	return "Basic realm=\"" + basic.realm + "\""
}

type digestAuth struct {
	userName string
	passwd   string
	realm    string
	nonce    string
	uri      string
	method   string
}

func (digest *digestAuth) setUri(url string) {
	digest.uri = url
}
func (digest *digestAuth) setMethod(method string) {
	digest.method = method
}

func (digest *digestAuth) setUserInfo(userName, passwd string) {
	digest.userName = userName
	digest.passwd = passwd
}

func (digest *digestAuth) setRealm(realm string) {
	digest.realm = realm
}

func (digest *digestAuth) createNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rtsp: crypto/rand failed: " + err.Error())
	}
	digest.nonce = hex.EncodeToString(b[:])
	return digest.nonce
}

func (digest *digestAuth) wwwAuthenticate() string {
	return "Digest realm=\"" + digest.realm + "\",nonce=\"" + digest.createNonce() + "\""
}

// decode reads realm and nonce out of a digest challenge. A challenge without
// a nonce can not be answered, answering it anyway would only produce a second
// 401 with a confusing reason.
func (digest *digestAuth) decode(challenge string) error {
	elems := strings.Split(strings.TrimPrefix(strings.TrimSpace(challenge), "Digest"), ",")
	hasNonce := false
	for i := 0; i < len(elems); i++ {
		elem := strings.TrimSpace(elems[i])
		kv := strings.SplitN(elem, "=", 2)
		if len(kv) < 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.Trim(strings.TrimSpace(kv[1]), "\"")
		switch key {
		case "realm":
			digest.realm = value
		case "nonce":
			digest.nonce = value
			hasNonce = true
		}
	}
	if !hasNonce {
		return errors.New("rtsp: digest challenge without a nonce: " + challenge)
	}
	return nil
}

// response=md5(md5(username:realm:password):nonce:md5(method:url));
func (digest *digestAuth) authenticateInfo() string {
	response := digest.makeResponse()
	digestInfo := fmt.Sprintf("Digest username=\"%s\", realm=\"%s\", nonce=\"%s\", uri=\"%s\", response=\"%s\"",
		digest.userName, digest.realm, digest.nonce, digest.uri, response)
	return digestInfo
}

func (digest *digestAuth) makeResponse() string {
	return digest.makeResponseForUri(digest.uri)
}

func (digest *digestAuth) makeResponseForUri(uri string) string {
	str1 := digest.userName + ":" + digest.realm + ":" + digest.passwd
	str2 := digest.method + ":" + uri
	md5Bytes1 := md5.Sum([]byte(str1))
	md5Bytes2 := md5.Sum([]byte(str2))
	md5str1 := fmt.Sprintf("%x", md5Bytes1)
	md5str2 := fmt.Sprintf("%x", md5Bytes2)
	str3 := md5str1 + ":" + digest.nonce + ":" + md5str2
	md5Bytes3 := md5.Sum([]byte(str3))
	return fmt.Sprintf("%x", md5Bytes3)
}

func (digest *digestAuth) check(authorization string) bool {
	if digest.nonce == "" {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(authorization), " ", 2)
	if len(parts) < 2 || parts[0] != "Digest" {
		return false
	}
	digestField := strings.TrimSpace(parts[1])
	response := ""
	clientUri := ""
	hasUser := false
	hasNonce := false
	hasResponse := false
	items := strings.Split(digestField, ",")
	for _, item := range items {
		kv := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(kv) < 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), "\"")
		switch k {
		case "username":
			hasUser = true
			if subtle.ConstantTimeCompare([]byte(v), []byte(digest.userName)) != 1 {
				return false
			}
		case "realm":
		case "nonce":
			hasNonce = true
			if subtle.ConstantTimeCompare([]byte(v), []byte(digest.nonce)) != 1 {
				return false
			}
		case "uri":
			clientUri = v
		case "response":
			hasResponse = true
			response = v
		}
	}
	if !hasUser || !hasNonce || !hasResponse {
		return false
	}
	uri := digest.uri
	if uri != "" {
		if clientUri != "" && clientUri != uri {
			return false
		}
	} else {
		uri = clientUri
	}
	expected := digest.makeResponseForUri(uri)
	return subtle.ConstantTimeCompare([]byte(response), []byte(expected)) == 1
}
