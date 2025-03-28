package parse

import (
	"strings"
)

type Env []string

func (e Env) Get(name string) string {
	v, _ := e.Lookup(name)
	return v
}

func (e Env) Has(name string) bool {
	_, ok := e.Lookup(name)
	return ok
}

func (e Env) Lookup(name string) (string, bool) {
	prefix := name + "="
	for _, pair := range e {
		if strings.HasPrefix(pair, prefix) {
			return pair[len(prefix):], true
		}
	}
	return "", false
}
