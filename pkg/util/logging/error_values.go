/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logging

import (
	"reflect"
	"slices"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	// maxErrorDepth bounds how deep an error tree is searched for log values.
	maxErrorDepth = 64
	// maxErrorNodes bounds how many errors are searched for the log values of
	// one log record. It stops the search of a tree that branches at every
	// level, or that contains itself.
	maxErrorNodes = 1024
	// maxErrorValues bounds the number of key-value pairs taken from the errors
	// of one log record.
	maxErrorValues = 32
)

// ErrorWithLogValues is an error that carries key-value pairs for the record
// that eventually logs it. CustomLogProcessor adds the pairs to that record.
type ErrorWithLogValues interface {
	error
	// LogValues returns alternating keys and values, as passed to a logr.Logger.
	LogValues() []any
}

type errorWithLogValues struct {
	err           error
	keysAndValues []any
}

// WithErrorValues returns err with keysAndValues attached, so that a function
// can return an error together with its structured context instead of logging
// the error itself. The message of err is unchanged, and errors.Is and
// errors.As see through the returned error.
//
// The values reach only loggers that use CustomLogProcessor, so the message
// must still identify the failure on its own.
//
// It returns nil if err is nil.
func WithErrorValues(err error, keysAndValues ...any) error {
	if err == nil {
		return nil
	}
	if len(keysAndValues) == 0 {
		return err
	}
	return &errorWithLogValues{err: err, keysAndValues: slices.Clone(keysAndValues)}
}

func (e *errorWithLogValues) Error() string {
	return e.err.Error()
}

func (e *errorWithLogValues) Unwrap() error {
	return e.err
}

func (e *errorWithLogValues) LogValues() []any {
	return slices.Clone(e.keysAndValues)
}

type logValue struct {
	key   string
	value any
}

// errorValues accumulates the log values found in the errors of a log record.
type errorValues struct {
	pairs []logValue
	// truncated reports that pairs can be incomplete: a limit stopped the
	// search or dropped a pair, or reading an error panicked.
	truncated bool
	// visited counts the errors searched so far.
	visited int
}

// collect appends the log values of err and of every error it wraps, outermost
// first. It visits each node of the error tree rather than using errors.As,
// which stops at the first match.
func (v *errorValues) collect(err error, depth int) {
	if err == nil {
		return
	}
	if depth > maxErrorDepth || v.visited >= maxErrorNodes {
		v.truncated = true
		return
	}
	v.visited++
	v.addValuesOf(err)
	// The methods of an error that holds a nil pointer usually panic. Recover,
	// so that the search continues with the other errors of the tree. zap
	// recovers the same way when it encodes the error.
	defer func() {
		if recover() != nil {
			v.truncated = true
		}
	}()
	switch e := err.(type) {
	case interface{ Unwrap() error }:
		v.collect(e.Unwrap(), depth+1)
	case interface{ Unwrap() []error }:
		v.collectAll(e.Unwrap(), depth+1)
	case utilerrors.Aggregate:
		// Aggregate does not implement Unwrap.
		v.collectAll(e.Errors(), depth+1)
	}
}

func (v *errorValues) collectAll(errs []error, depth int) {
	for _, err := range errs {
		if v.visited >= maxErrorNodes {
			v.truncated = true
			return
		}
		v.collect(err, depth)
	}
}

// addValuesOf appends the log values that err carries itself. A panic in
// LogValues loses only these values.
func (v *errorValues) addValuesOf(err error) {
	defer func() {
		if recover() != nil {
			v.truncated = true
		}
	}()
	if e, ok := err.(ErrorWithLogValues); ok {
		v.add(e.LogValues())
	}
}

// add appends keysAndValues. As zapr does for a log call, it ignores a key
// without a value, and everything from the first key that is not a string.
func (v *errorValues) add(keysAndValues []any) {
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			return
		}
		pair := logValue{key: key, value: keysAndValues[i+1]}
		if containsPair(v.pairs, pair) {
			// An error visited earlier carries the same key and value.
			continue
		}
		if len(v.pairs) >= maxErrorValues {
			v.truncated = true
			return
		}
		v.pairs = append(v.pairs, pair)
	}
}

func containsKey(pairs []logValue, key string) bool {
	return slices.ContainsFunc(pairs, func(pair logValue) bool {
		return pair.key == key
	})
}

func containsPair(pairs []logValue, pair logValue) bool {
	return slices.ContainsFunc(pairs, func(other logValue) bool {
		return other.key == pair.key && sameValue(other.value, pair.value)
	})
}

// sameValue reports whether a and b are equal. Values that cannot be compared,
// such as slices, are never equal.
func sameValue(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	return va.Comparable() && vb.Comparable() && va.Equal(vb)
}
