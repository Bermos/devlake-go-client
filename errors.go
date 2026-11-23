package devlake

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

// ErrNotFound is matched (via errors.Is) by API errors with status 404.
var ErrNotFound = errors.New("devlake: not found")

// APIError is returned when DevLake answers with a non-2xx status.
type APIError struct {
	StatusCode int
	// Message is DevLake's error message, if the body was DevLake's JSON
	// error envelope.
	Message string
	// Causes holds DevLake's error cause chain, if any.
	Causes []string
	// Body is the raw response body.
	Body []byte
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(string(e.Body))
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
	}
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("devlake: HTTP %d: %s", e.StatusCode, msg)
}

// Is makes errors.Is(err, ErrNotFound) work.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// response is implemented by every generated *XxxResponse type.
type response interface {
	StatusCode() int
}

// CheckResponse turns a generated response into an error if its status is not
// 2xx. It is useful when calling the low-level client from [Client.API]:
//
//	resp, err := c.API().GetBlueprintsWithResponse(ctx, nil)
//	if err := devlake.CheckResponse(resp, err); err != nil { ... }
func CheckResponse(resp response, err error) error {
	if err != nil {
		return fmt.Errorf("devlake: %w", err)
	}
	if resp == nil {
		return errors.New("devlake: nil response")
	}
	return checkStatus(resp.StatusCode(), bodyOf(resp))
}

// bodyOf extracts the raw Body field every generated response type carries.
func bodyOf(resp response) []byte {
	v := reflect.ValueOf(resp)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName("Body")
	if !f.IsValid() || f.Kind() != reflect.Slice || f.Type().Elem().Kind() != reflect.Uint8 {
		return nil
	}
	return f.Bytes()
}
