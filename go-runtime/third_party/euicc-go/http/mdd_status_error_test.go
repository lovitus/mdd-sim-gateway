package http

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type mddTransport func(*http.Request) (*http.Response, error)

func (f mddTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mddBody struct {
	io.Reader
	closed bool
}

func (b *mddBody) Close() error { b.closed = true; return nil }

func TestMDDHTTPStatusClosesBody(t *testing.T) {
	body := &mddBody{Reader: strings.NewReader("private upstream response")}
	c := Client{Client: &http.Client{Transport: mddTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: body, Header: http.Header{}}, nil
	})}}
	err := c.SendRequest(&url.URL{Scheme: "https", Host: "example.com"}, struct{}{}, &struct{}{})
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != 403 || !body.closed {
		t.Fatal("status or body ownership lost")
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("response body leaked")
	}
}
