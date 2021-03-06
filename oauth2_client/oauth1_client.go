package oauth2_client

import (
    "bytes"
    "crypto/hmac"
    "crypto/rand"
    "crypto/sha1"
    "encoding/base64"
    "encoding/binary"
    "errors"
    "fmt"
    "github.com/pomack/jsonhelper.go/jsonhelper"
    "io"
    "io/ioutil"
    "net/http"
    "net/url"
    "sort"
    "strconv"
    "strings"
    "time"
)

// Credentials represents client, temporary and token credentials.
type AuthToken interface {
    // Also known as consumer key or access token.
    Token() string
    // Also known as consumer secret or access token secret.
    Secret() string
    SetToken(value string)
    SetSecret(value string)
}

type stdAuthToken struct {
    token  string
    secret string
}

func NewStandardAuthToken() AuthToken {
    return &stdAuthToken{}
}

type OAuth1Client interface {
    OAuth2Client
    CurrentCredentials() AuthToken
    SetCurrentCredentials(value AuthToken)
    Realm() string
    ConsumerKey() string
    ConsumerSecret() string
    RequestUrl() string
    RequestUrlMethod() string
    RequestUrlProtected() bool
    AccessUrl() string
    AccessUrlMethod() string
    AccessUrlProtected() bool
    AuthorizationUrl() string
    AuthorizedResourceProtected() bool
    CallbackUrl() string
    ParseRequestTokenResult(value string) (AuthToken, error)
    ParseAccessTokenResult(value string) (AuthToken, error)
}

type stdOAuth1Client struct {
    client             *http.Client
    currentCredentials AuthToken
    serviceName        string
    realm              string
    consumerKey        string
    consumerSecret     string
    callbackUrl        string
}

type RequestHandler func(*http.Response, *http.Request, error)

type oauth1SecretInfo struct {
    service string
    token   string
    secret  string
}

// nonce returns a unique string.
func newNonce() string {
    nonceLock.Lock()
    defer nonceLock.Unlock()
    if nonceCounter == 0 {
        binary.Read(rand.Reader, binary.BigEndian, &nonceCounter)
    }
    result := strconv.FormatUint(nonceCounter, 16)
    nonceCounter += 1
    return result
}

func oauthEncode(text string) string {
    s := url.QueryEscape(text)
    count := 0
    for _, c := range s {
        switch c {
        case '!', '*', '\'', '(', ')': // characters that are not escaped in url.QueryEscape() but need to be for OAuth encoding
            count++
        }
    }
    if count > 0 {
        a := make([]byte, len(s)+count*2)
        pos := 0
        l := len(s)
        for i := 0; i < l; i++ {
            c := s[i]
            switch c {
            case '!', '*', '\'', '(', ')':
                a[pos] = '%'
                a[pos+1] = "0123456789ABCDEF"[c>>4]
                a[pos+2] = "0123456789ABCDEF"[c&15]
                pos += 3
            default:
                a[pos] = c
                pos++
            }
        }
        s = string(a)
    }
    return s
}

func getKeys(m url.Values) []string {
    arr := make([]string, len(m))
    i := 0
    for k := range m {
        arr[i] = k
        i++
    }
    return arr
}

func getSortedKeys(m url.Values) []string {
    arr := getKeys(m)
    sort.Strings(arr)
    return arr
}

func (p *stdAuthToken) Token() string          { return p.token }
func (p *stdAuthToken) Secret() string         { return p.secret }
func (p *stdAuthToken) SetToken(value string)  { p.token = value }
func (p *stdAuthToken) SetSecret(value string) { p.secret = value }

func (p *stdOAuth1Client) Client() *http.Client {
    if p.client == nil {
        p.client = new(http.Client)
    }
    return p.client
}
func (p *stdOAuth1Client) CurrentCredentials() AuthToken         { return p.currentCredentials }
func (p *stdOAuth1Client) Realm() string                         { return p.realm }
func (p *stdOAuth1Client) ConsumerKey() string                   { return p.consumerKey }
func (p *stdOAuth1Client) ConsumerSecret() string                { return p.consumerSecret }
func (p *stdOAuth1Client) CallbackUrl() string                   { return p.callbackUrl }
func (p *stdOAuth1Client) SetCurrentCredentials(value AuthToken) { p.currentCredentials = value }

func oauth1PrepareRequest(p OAuth1Client, credentials AuthToken, method, uri string, additional_params url.Values, timestamp time.Time, nonce string) url.Values {
    if len(method) <= 0 {
        method = GET
    }
    theurl, _ := url.Parse(uri)
    params := make(url.Values)
    if len(p.Realm()) > 0 {
        params.Set("realm", p.Realm())
    }
    params.Set("oauth_consumer_key", p.ConsumerKey())
    params.Set("oauth_signature_method", "HMAC-SHA1")
    if timestamp.IsZero() {
        timestamp = time.Now().UTC()
    }
    params.Set("oauth_timestamp", strconv.FormatInt(timestamp.Unix(), 10))
    if len(nonce) <= 0 {
        nonce = newNonce()
    }
    params.Set("oauth_nonce", nonce)
    params.Set("oauth_version", "1.0")

    if credentials != nil && len(credentials.Token()) > 0 {
        params.Set("oauth_token", credentials.Token())
    } else if len(p.CallbackUrl()) > 0 {
        params.Set("oauth_callback", p.CallbackUrl())
    }
    if theurl != nil && len(theurl.Query()) > 0 {
        for k, arr := range theurl.Query() {
            for _, v := range arr {
                params.Add(k, v)
            }
        }
    }
    if additional_params != nil && len(additional_params) > 0 {
        for k, arr := range additional_params {
            if len(arr) > 0 {
                params.Del(k)
                for _, v := range arr {
                    params.Add(k, v)
                }
            }
        }
    }
    params_arr := make([]string, 0, 10)
    for _, k := range getSortedKeys(params) {
        arr := params[k]
        ek := oauthEncode(k)
        for _, v := range arr {
            params_arr = append(params_arr, strings.Join([]string{ek, oauthEncode(v)}, "="))
        }
    }
    params_str := strings.Join(params_arr, "&")
    message := strings.Join([]string{method, oauthEncode(strings.TrimSpace(strings.SplitN(uri, "?", 2)[0])), oauthEncode(params_str)}, "&")
    secret := ""
    if credentials != nil && len(credentials.Secret()) > 0 {
        secret = credentials.Secret()
    }
    key := strings.Join([]string{p.ConsumerSecret(), secret}, "&")
    h := hmac.New(sha1.New, []byte(key))
    h.Write([]byte(message))
    sum := h.Sum(nil)

    encodedSum := make([]byte, base64.StdEncoding.EncodedLen(len(sum)))
    base64.StdEncoding.Encode(encodedSum, sum)
    signature := strings.TrimSpace(string(encodedSum))
    LogDebug("Generated signature: \"", signature, "\", with key: \"", key, "\" and message: \"", message, "\"")
    params.Set("oauth_signature", signature)
    return params
}

func oauth1GenerateRequest(p OAuth1Client, credentials AuthToken, headers http.Header, method, uri string, additional_params url.Values, protected bool) (*http.Request, error) {
    finalUri, params := splitUrl(uri, additional_params)
    v := oauth1PrepareRequest(p, credentials, method, uri, params, time.Time{}, "")
    var r io.Reader
    if protected {
        if headers == nil {
            headers = make(http.Header)
        }
        realm := v.Get("realm")
        oauth_nonce := v.Get("oauth_nonce")
        oauth_timestamp := v.Get("oauth_timestamp")
        oauth_version := v.Get("oauth_version")
        oauth_signature_method := v.Get("oauth_signature_method")
        oauth_consumer_key := v.Get("oauth_consumer_key")
        oauth_token := v.Get("oauth_token")
        oauth_signature := v.Get("oauth_signature")
        v.Del("realm")
        v.Del("oauth_nonce")
        v.Del("oauth_timestamp")
        v.Del("oauth_version")
        v.Del("oauth_signature_method")
        v.Del("oauth_consumer_key")
        v.Del("oauth_token")
        v.Del("oauth_signature")
        oauth_realm := ""
        if len(realm) > 0 {
            oauth_realm = fmt.Sprint("realm=\"", url.QueryEscape(realm), "\",")
        }
        headers.Set("Authorization", fmt.Sprintf(`OAuth %soauth_nonce="%s",oauth_timestamp="%s",oauth_version="%s",oauth_signature_method="%s",oauth_consumer_key="%s",oauth_token="%s",oauth_signature="%s"`, oauth_realm, url.QueryEscape(oauth_nonce), url.QueryEscape(oauth_timestamp), url.QueryEscape(oauth_version), url.QueryEscape(oauth_signature_method), url.QueryEscape(oauth_consumer_key), url.QueryEscape(oauth_token), url.QueryEscape(oauth_signature)))
    }
    if method == GET {
        if protected {
            finalUri = MakeUrl(uri, additional_params)
        } else {
            finalUri = MakeUrl(finalUri, v)
        }
        r = nil
    } else {
        r = bytes.NewBufferString(v.Encode())
        if protected {
            finalUri = uri
        } else {
            finalUri = uri
        }
        //finalUri = uri
    }
    req, err := http.NewRequest(method, finalUri, r)
    if req != nil {
        req.Header = headers
    }
    return req, err
}

func OAuth1MakeSyncRequest(p OAuth1Client, credentials AuthToken, headers http.Header, method, uri string, additional_params url.Values, protected bool) (*http.Response, *http.Request, error) {
    req, err := oauth1GenerateRequest(p, credentials, headers, method, uri, additional_params, protected)
    if err != nil {
        return nil, req, err
    }
    return MakeRequest(p, req)
}

func MakeAsyncRequest(p OAuth1Client, req *http.Request, handler RequestHandler) {
    resp, _, err := MakeRequest(p, req)
    if handler != nil {
        handler(resp, req, err)
    }
}

func parseRequestTokenResult(p OAuth1Client, value string) (AuthToken, error) {
    return p.ParseRequestTokenResult(value)
}
func parseAccessTokenResult(p OAuth1Client, value string) (AuthToken, error) {
    return p.ParseAccessTokenResult(value)
}

func defaultOAuth1ParseAuthToken(value string) (AuthToken, error) {
    m, err := url.ParseQuery(value)
    var cred AuthToken
    if m != nil {
        cred = &stdAuthToken{token: m.Get("oauth_token"), secret: m.Get("oauth_token_secret")}
    } else {
        cred = &stdAuthToken{}
    }
    return cred, err
}

func getAuthToken(p OAuth1Client) (AuthToken, error) {
    resp, _, err := OAuth1MakeSyncRequest(p, nil, nil, p.RequestUrlMethod(), p.RequestUrl(), nil, p.RequestUrlProtected())
    if err != nil {
        return nil, err
    }
    body_bytes, err := ioutil.ReadAll(resp.Body)
    body := string(body_bytes)
    credentials, err := parseRequestTokenResult(p, body)
    if credentials != nil && len(credentials.Token()) > 0 && len(credentials.Secret()) > 0 {
        if oauth1TokenSecretMap == nil {
            oauth1TokenSecretMap = make(map[string]*oauth1SecretInfo)
        }
        oauth1TokenSecretMap[credentials.Token()] = &oauth1SecretInfo{
            service: p.ServiceId(),
            token:   credentials.Token(),
            secret:  credentials.Secret(),
        }
    } else if err == nil && len(body) > 0 {
        err = errors.New(body)
    }
    return credentials, err
}

func oauth1RequestToken(p OAuth1Client, client *http.Client, credentials AuthToken, verifier string) (AuthToken, string, error) {
    if oauth1TokenSecretMap == nil {
        oauth1TokenSecretMap = make(map[string]*oauth1SecretInfo)
    }
    auth_token, _ := url.QueryUnescape(credentials.Token())
    auth_verifier, _ := url.QueryUnescape(verifier)

    auth_secret_info, _ := oauth1TokenSecretMap[auth_token]
    auth_secret := ""
    if auth_secret_info != nil {
        auth_secret = auth_secret_info.secret
    }
    if len(auth_secret) <= 0 && len(credentials.Secret()) > 0 {
        auth_secret = credentials.Secret()
    }
    LogDebug("Using auth_token: ", auth_token, ", auth_secret: ", auth_secret, ", oauth_verifier: ", auth_verifier)
    cred := &stdAuthToken{token: auth_token, secret: auth_secret}
    additional_params := make(url.Values)
    if len(auth_verifier) > 0 {
        additional_params.Set("oauth_verifier", auth_verifier)
    }
    resp, _, err := OAuth1MakeSyncRequest(p, cred, nil, p.AccessUrlMethod(), p.AccessUrl(), additional_params, p.AccessUrlProtected())
    var err2 error
    var body string
    if resp != nil && resp.Body != nil {
        var body_bytes []byte
        body_bytes, err2 = ioutil.ReadAll(resp.Body)
        body = string(body_bytes)
    }
    c, err3 := parseAccessTokenResult(p, body)
    if c != nil && len(c.Token()) > 0 && len(c.Secret()) > 0 {
        oauth1TokenSecretMap[c.Token()] = &oauth1SecretInfo{
            service: p.ServiceId(),
            token:   c.Token(),
            secret:  c.Secret(),
        }
    } else if err2 == nil && len(body) > 0 {
        err2 = errors.New(body)
    }
    if err == nil {
        err = err2
        if err == nil {
            err = err3
        }
    }
    return c, body, err
}

func (p *stdOAuth1Client) ParseRequestTokenResult(value string) (AuthToken, error) {
    return defaultOAuth1ParseAuthToken(value)
}

func (p *stdOAuth1Client) ParseAccessTokenResult(value string) (AuthToken, error) {
    return defaultOAuth1ParseAuthToken(value)
}

// AuthorizationURL returns the full authorization URL.
func oauth1GenerateAuthorizationUrl(p OAuth1Client, temporaryCredentials AuthToken) string {
    authUrl := p.AuthorizationUrl()
    if strings.Contains(authUrl, "?") {
        return authUrl + "&oauth_token=" + string(oauthEncode(temporaryCredentials.Token()))
    }
    return authUrl + "?oauth_token=" + string(oauthEncode(temporaryCredentials.Token()))
}

func oauth1GenerateRequestTokenUrl(p OAuth1Client, properties jsonhelper.JSONObject) string {
    if properties == nil {
        properties = jsonhelper.NewJSONObject()
    }
    cred, err := getAuthToken(p)
    LogDebugf("Received credentials: %T -> %v", cred, cred)
    LogDebug("Received err: ", err)
    if cred == nil || err != nil {
        return ""
    }
    return oauth1GenerateAuthorizationUrl(p, cred)
}

func oauth1RequestTokenGranted(p OAuth1Client, req *http.Request) bool {
    if req == nil {
        return false
    }
    q := req.URL.Query()
    token := q.Get("oauth_token")
    verifier := q.Get("oauth_verifier")
    // apparently smugmug.com doesn't specify an oauth_verifier, so don't require it
    if len(token) <= 0 {
        return false
    }
    tempCredentials := &stdAuthToken{token: token}
    newCredentials, _, err := oauth1RequestToken(p, nil, tempCredentials, verifier)
    if err != nil || newCredentials == nil {
        return false
    }
    p.SetCurrentCredentials(newCredentials)
    return true
}

func oauth1ExchangeRequestTokenForAccess(p OAuth1Client, req *http.Request) error {
    if req == nil {
        return errors.New("Request cannot be nil")
    }
    q := req.URL.Query()
    token := q.Get("oauth_token")
    verifier := q.Get("oauth_verifier")
    // apparently smugmug.com doesn't specify an oauth_verifier, so don't require it
    if len(token) <= 0 {
        return errors.New("Expected oauth_token")
    }
    secret_info, _ := oauth1TokenSecretMap[token]
    secret := ""
    if secret_info != nil {
        secret = secret_info.secret
    }
    tempCredentials := &stdAuthToken{token: token, secret: secret}
    newCredentials, body, err := oauth1RequestToken(p, nil, tempCredentials, verifier)
    if err != nil {
        return err
    }
    if newCredentials != nil && len(newCredentials.Token()) > 0 && len(newCredentials.Secret()) > 0 {
        LogInfof("Setting current credentials to: %T -> %v", newCredentials, newCredentials)
        p.SetCurrentCredentials(newCredentials)
    } else if len(body) > 0 {
        return errors.New(body)
    }
    return nil
}

func oauth1CreateAuthorizedRequest(p OAuth1Client, method string, headers http.Header, uri string, query url.Values, r io.Reader) (*http.Request, error) {
    if len(method) <= 0 {
        method = GET
    }
    method = strings.ToUpper(method)
    if headers == nil {
        headers = make(http.Header)
    }
    if query == nil {
        query = make(url.Values)
    }
    return oauth1GenerateRequest(p, p.CurrentCredentials(), headers, method, uri, query, p.AuthorizedResourceProtected())
}
