package httputil

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

func long2ip(ip int64) string {
	return fmt.Sprintf("%d.%d.%d.%d",
		(ip>>24)&0xFF,
		(ip>>16)&0xFF,
		(ip>>8)&0xFF,
		ip&0xFF)
}

func randomIP() string {
	return long2ip(randRange(1884815360, 1884890111))
}

func randRange(min, max int64) int64 {
	diff := max - min
	move := rand.Int63n(diff)
	randNum := min + move
	return randNum
}

type (
	option struct {
		ctx      context.Context
		host     string
		body     io.Reader
		timeout  time.Duration
		requests []func(*http.Request)
	}
	Option func(*option) error

	Client struct {
		client *resty.Client
	}
	ClientOption func(*http.Client)
)

const NeverTimeout = time.Duration(-1)

func WithContext(ctx context.Context) Option {
	return func(opt *option) error {
		opt.ctx = ctx
		return nil
	}
}

func WithHeaders(headers map[string]string) Option {
	return func(opt *option) error {
		if opt.requests == nil {
			opt.requests = make([]func(*http.Request), 0)
		}
		opt.requests = append(opt.requests, func(req *http.Request) {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		})
		return nil
	}
}

func WithRequest(reqFunc func(req *http.Request)) Option {
	return func(opt *option) error {
		if opt.requests == nil {
			opt.requests = make([]func(*http.Request), 0)
		}
		opt.requests = append(opt.requests, reqFunc)
		return nil
	}
}

func WithHost(host string) Option {
	return func(opt *option) error {
		opt.host = host
		return nil
	}
}

func WithBody(body io.Reader) Option {
	return func(opt *option) error {
		opt.body = body
		return nil
	}
}

func WithForm(data map[string]string) Option {
	return func(opt *option) error {
		form := url.Values{}
		for k, v := range data {
			form.Set(k, v)
		}
		opt.body = strings.NewReader(form.Encode())
		WithHeaders(map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		})(opt)
		return nil
	}
}

func WithJson(data any) Option {
	return func(opt *option) error {
		buf, err := json.Marshal(data)
		if err != nil {
			return err
		}
		opt.body = bytes.NewBuffer(buf)
		WithHeaders(map[string]string{
			"Content-Type": "application/json",
		})(opt)
		return nil
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(opt *option) error {
		opt.timeout = timeout
		return nil
	}
}

func WithNeverTimeout() Option {
	return func(opt *option) error {
		opt.timeout = NeverTimeout
		return nil
	}
}

func WithQueryParams(data map[string]string) Option {
	return func(opt *option) error {
		if opt.requests == nil {
			opt.requests = make([]func(*http.Request), 0)
		}
		opt.requests = append(opt.requests, func(req *http.Request) {
			q := req.URL.Query()
			for k, v := range data {
				q.Add(k, fmt.Sprintf("%v", v))
			}
			req.URL.RawQuery = q.Encode()
		})
		return nil
	}
}

func (d *Client) Request(method, url string, opts ...Option) (*http.Response, error) {
	opt := &option{}
	for _, fn := range opts {
		if err := fn(opt); err != nil {
			return nil, err
		}
	}

	if strings.HasPrefix(url, "/") && opt.host != "" {
		url = strings.TrimSuffix(opt.host, "/") + url
	}

	client := d.client
	if opt.timeout == NeverTimeout || opt.timeout > 0 || len(opt.requests) > 0 {
		httpClient := *d.client.GetClient()
		if opt.timeout == NeverTimeout || opt.timeout > 0 {
			if opt.timeout == NeverTimeout {
				httpClient.Timeout = 0
			} else {
				httpClient.Timeout = opt.timeout
			}
		}
		client = resty.NewWithClient(&httpClient)
	}

	req := client.R().SetDoNotParseResponse(true)
	if opt.ctx != nil {
		req.SetContext(opt.ctx)
	}
	if opt.body != nil {
		req.SetBody(opt.body)
	}

	headers := map[string]string{
		"X-Real-IP":  randomIP(),
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_5, AppleWebKit/605.1.15 (KHTML, like Gecko,",
	}
	req.SetHeaders(headers)

	if len(opt.requests) > 0 {
		client.SetPreRequestHook(func(_ *resty.Client, raw *http.Request) error {
			for _, fn := range opt.requests {
				fn(raw)
			}
			return nil
		})
	}

	resp, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	return resp.RawResponse, nil
}

func newClient() *http.Client {
	transport := &http.Transport{
		IdleConnTimeout:       120 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		Dial: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 60 * time.Second,
		}).Dial,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}
}

func WithClientTimeout(timeout time.Duration) ClientOption {
	return func(client *http.Client) {
		client.Timeout = timeout
	}
}

func New(opts ...ClientOption) *Client {
	httpClient := newClient()
	for _, opt := range opts {
		opt(httpClient)
	}
	return &Client{
		client: resty.NewWithClient(httpClient),
	}
}
