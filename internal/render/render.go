// Package render is the shared output layer for the CLI commands: pretty-JSON
// results on stdout, JSON errors on stderr, and owner-only file writes for
// credential-bearing output (kubeconfigs).
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
)

// JSON writes v as indented JSON to stdout.
func JSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(b))
	return err
}

// payloader is implemented by provider errors that carry a raw error body to be
// surfaced to the user verbatim (e.g. the DigitalOcean API error payload).
type payloader interface{ Payload() any }

// Error writes err as indented JSON to stderr. An error carrying a provider
// payload is rendered verbatim; any other error renders as {code, message}.
func Error(err error) {
	var body any
	var p payloader
	if errors.As(err, &p) {
		body = p.Payload()
	} else {
		body = map[string]string{"code": code(err), "message": err.Error()}
	}
	b, jerr := json.MarshalIndent(body, "", "  ")
	if jerr != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return
	}
	fmt.Fprintln(os.Stderr, string(b))
}

// code returns a short, stable identifier for an error's concrete type.
func code(err error) string {
	t := reflect.TypeOf(err)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "error"
	}
	return t.Name()
}

// WriteOwnerOnly writes content to path, creating it 0600 and refusing to follow
// a symlink at the target — for credential-bearing output like kubeconfigs.
func WriteOwnerOnly(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(content)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
