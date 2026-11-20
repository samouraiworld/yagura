package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// maxBodyBytes caps a request body.
const maxBodyBytes = 16 << 10

// paramError is a request the API cannot read.
type paramError struct {
	code int
	msg  string
}

func (e *paramError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &paramError{code: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

// readParams merges the parameters of the query string and of a JSON or
// form body, then trims their values. A key given twice with different
// values is an error. On error it writes the response and returns false.
func readParams(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	p, err := params(w, r)
	if err != nil {
		var pe *paramError
		if !errors.As(err, &pe) {
			pe = &paramError{code: http.StatusBadRequest, msg: err.Error()}
		}
		writeError(w, pe.code, pe.msg)
		return nil, false
	}
	return p, true
}

func params(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	out := map[string]string{}
	add := func(k, v string) error {
		v = strings.TrimSpace(v)
		if old, ok := out[k]; ok && old != v {
			return badRequest("parameter %s is given twice with different values", capped(k))
		}
		out[k] = v
		return nil
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, badRequest("invalid query string")
	}
	for k, vs := range query {
		for _, v := range vs {
			if err := add(k, v); err != nil {
				return nil, err
			}
		}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &paramError{code: http.StatusRequestEntityTooLarge, msg: fmt.Sprintf("body is larger than %d KiB", maxBodyBytes>>10)}
		}
		return nil, badRequest("cannot read the body")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return out, nil
	}
	kind, err := bodyKind(r.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, err
	}
	if kind == "json" {
		return out, jsonParams(body, add)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, badRequest("invalid form body")
	}
	for k, vs := range form {
		for _, v := range vs {
			if err := add(k, v); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// bodyKind returns "json" or "form". A body with no Content-Type or with
// text/plain is sniffed: "{" starts JSON, anything else is a form.
func bodyKind(contentType string, body []byte) (string, error) {
	mt := ""
	if contentType != "" {
		var err error
		if mt, _, err = mime.ParseMediaType(contentType); err != nil {
			return "", &paramError{code: http.StatusUnsupportedMediaType, msg: "invalid Content-Type"}
		}
	}
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		return "json", nil
	case mt == "application/x-www-form-urlencoded":
		return "form", nil
	case mt == "" || mt == "text/plain":
		if body[0] == '{' {
			return "json", nil
		}
		return "form", nil
	}
	return "", &paramError{code: http.StatusUnsupportedMediaType,
		msg: "Content-Type must be application/json or application/x-www-form-urlencoded"}
}

// jsonParams reads a JSON object whose values are strings or numbers; null
// counts as absent.
func jsonParams(body []byte, add func(k, v string) error) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return badRequest("body must be a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return badRequest("body has data after the JSON object")
	}
	for k, v := range obj {
		var s string
		switch v := v.(type) {
		case nil:
			continue
		case string:
			s = v
		case json.Number:
			s = v.String()
		default:
			return badRequest("parameter %s must be a string or a number", capped(k))
		}
		if err := add(k, s); err != nil {
			return err
		}
	}
	return nil
}
