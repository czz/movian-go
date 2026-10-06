package http

// HTTPRequester defines HTTP client operations.
type HTTPRequester interface {
	Get(url string) (*HTTPResponse, error)
	Post(url string, body []byte) (*HTTPResponse, error)
	Head(url string) (*HTTPResponse, error)
}

// HTTPRequestConstructor defines HTTP request construction operations.
type HTTPRequestConstructor interface {
	SetMethod(method string)
	SetURL(url string)
	SetHeader(key, value string)
	SetBody(body []byte)
	Build() *HTTPRequest
}

// HTTPResponseHandler defines HTTP response handling operations.
type HTTPResponseHandler interface {
	StatusCode() int
	Headers() map[string]string
	Body() []byte
	BodyString() string
}
