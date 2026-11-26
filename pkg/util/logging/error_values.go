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
	// maxErrorNodes bounds how many errors are searched for the log values of
	// one log record, stopping deep, branching, or cyclic error trees.
	maxErrorNodes = 1024
	// maxErrorValues bounds the number of key-value pairs taken from the errors
	// of one log record, and how many pairs of one ErrorWithValues call are read.
	maxErrorValues = 32
)

type errorWithValues struct {
	err           error
	keysAndValues []any
}

// ErrorWithValues returns err with keysAndValues attached, so that a function
// can return an error together with its structured context instead of logging
// the error itself. keysAndValues are alternating string keys and values, as in
// a logr call. A value is logged under its key, or under "kueueErrorContext" if
// the record already has that key.
//
// The message of err is unchanged, and errors.Is and errors.As see through the
// returned error. As with fmt.Errorf("%w"), zap's errorCauses and errorVerbose
// fields are not written for the wrapped error.
//
// The values reach only loggers that use CustomLogProcessor, so the message
// must still identify the failure on its own. Pass cheap, safe values such as
// klog.KObj(obj), strings, and numbers.
//
// It returns nil if err is nil.
func ErrorWithValues(err error, keysAndValues ...any) error {
	if err == nil {
		return nil
	}
	if len(keysAndValues) == 0 {
		return err
	}
	return &errorWithValues{err: err, keysAndValues: slices.Clone(keysAndValues)}
}

func (e *errorWithValues) Error() string {
	return e.err.Error()
}

func (e *errorWithValues) Unwrap() error {
	return e.err
}

type keyValue struct {
	key   string
	value any
}

// errorValueCollector accumulates the log values found in the errors of a log record.
type errorValueCollector struct {
	pairs []keyValue
	// incomplete reports that pairs can be incomplete: a limit stopped the
	// search or dropped a pair, keysAndValues was malformed, or reading an
	// error panicked.
	incomplete bool
	// visited counts the errors searched so far.
	visited int
}

// collect appends the log values of err and of every error it wraps, depth-first:
// an error before the errors it wraps, and joined errors in their order. It
// visits each node of the error tree.
func (c *errorValueCollector) collect(err error) {
	if err == nil {
		return
	}
	if c.visited >= maxErrorNodes {
		c.incomplete = true
		return
	}
	c.visited++
	// The methods of an error that holds a nil pointer (for example a typed-nil
	// *net.OpError or *url.Error) usually panic. Recover, so that the search
	// continues with the other errors of the tree. zap recovers the same way
	// when it encodes err.Error().
	defer func() {
		if recover() != nil {
			c.incomplete = true
		}
	}()
	switch e := err.(type) {
	case *errorWithValues:
		c.merge(e.keysAndValues)
		c.collect(e.err)
	case interface{ Unwrap() error }:
		c.collect(e.Unwrap())
	case interface{ Unwrap() []error }:
		c.collectAll(e.Unwrap())
	case utilerrors.Aggregate:
		// Aggregate does not implement Unwrap.
		c.collectAll(e.Errors())
	}
}

func (c *errorValueCollector) collectAll(errs []error) {
	for _, err := range errs {
		c.collect(err)
		if c.incomplete && c.visited >= maxErrorNodes {
			return
		}
	}
}

// merge appends keysAndValues. It ignores a trailing key without a value and
// everything from the first key that is not a string, and marks the collector
// incomplete.
//
// Unlike zapr, it does not emit a separate dpanic record.
// loggercheck (linter) reports an odd number of arguments in a literal ErrorWithValues
// call; nothing checks the type of a key.
func (c *errorValueCollector) merge(keysAndValues []any) {
	if c.incomplete && len(c.pairs) >= maxErrorValues {
		return
	}
	// Cap at 2*maxErrorValues (alternating keys and values) to bound CPU work on large slices.
	if len(keysAndValues) > 2*maxErrorValues {
		keysAndValues = keysAndValues[:2*maxErrorValues]
		c.incomplete = true
	}
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 >= len(keysAndValues) {
			c.incomplete = true
			return
		}
		key, ok := keysAndValues[i].(string)
		if !ok {
			c.incomplete = true
			return
		}
		pair := keyValue{key: key, value: keysAndValues[i+1]}
		if containsPair(c.pairs, pair) {
			// An earlier pair carries the same key and value.
			continue
		}
		if len(c.pairs) >= maxErrorValues {
			c.incomplete = true
			return
		}
		c.pairs = append(c.pairs, pair)
	}
}

func containsPair(pairs []keyValue, pair keyValue) bool {
	return slices.ContainsFunc(pairs, func(other keyValue) bool {
		return other.key == pair.key && sameValue(other.value, pair.value)
	})
}

// sameValue reports whether a and b are equal. Values that cannot be compared,
// such as slices or maps, are never equal.
func sameValue(a, b any) (same bool) {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	t := reflect.TypeOf(a)
	if t != reflect.TypeOf(b) || !t.Comparable() {
		return false
	}
	// A comparable struct or array type can hold an interface whose dynamic
	// value is not comparable; == panics for it.
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}
