package nativerec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// daemon is the recordings API as the window uses it, with the owner token.
type daemon struct {
	url, token string
	c          *http.Client
}

// httpError is the daemon's refusal: its status and {error} sentence.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

type activeAnswer struct {
	Recording *model.Recording      `json:"recording"`
	Settings  config.RecorderConfig `json:"settings"`
}

func (d *daemon) active() (activeAnswer, error) {
	var a activeAnswer
	err := d.do("GET", "/api/recordings/active", nil, &a)
	a.Settings = a.Settings.Resolved()
	return a, err
}

func (d *daemon) begin(req recorder.BeginRequest) (model.Recording, error) {
	var r model.Recording
	return r, d.do("POST", "/api/recordings", req, &r)
}

func (d *daemon) state(id, state string) (model.Recording, error) {
	var r model.Recording
	return r, d.do("POST", "/api/recordings/"+url.PathEscape(id)+"/state", map[string]string{"state": state}, &r)
}

func (d *daemon) native(id string, rep recorder.NativeReport) (model.Recording, error) {
	var r model.Recording
	return r, d.do("POST", "/api/recordings/"+url.PathEscape(id)+"/native", rep, &r)
}

func (d *daemon) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.url+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := d.c.Do(req)
	if err != nil {
		return fmt.Errorf("the daemon is not reachable at %s", d.url)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &httpError{status: res.StatusCode, msg: msg}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
