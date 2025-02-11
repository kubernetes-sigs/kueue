package parser

import (
	"fmt"
	"strings"
)

// context context at parsing
type context struct {
	path string
}

var pathSpecialChars = []string{
	"$", "*", ".", "[", "]",
}

func containsPathSpecialChar(path string) bool {
	for _, char := range pathSpecialChars {
		if strings.Contains(path, char) {
			return true
		}
	}
	return false
}

func normalizePath(path string) string {
	if containsPathSpecialChar(path) {
		return fmt.Sprintf("'%s'", path)
	}
	return path
}

func (c *context) withChild(path string) *context {
	ctx := *c
	ctx.path = c.path + "." + normalizePath(path)
	return &ctx
}

func (c *context) withIndex(idx uint) *context {
	ctx := *c
	ctx.path = c.path + "[" + fmt.Sprint(idx) + "]"
	return &ctx
}

func newContext() *context {
	return &context{
		path: "$",
	}
}
