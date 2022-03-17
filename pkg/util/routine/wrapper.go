/*
Copyright 2022 The Kubernetes Authors.

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

package routine

// Wrapper is used to wrap a function that will run in a goroutine.
type Wrapper interface {
	Run(func())
}

var _ Wrapper = &wrapper{}

var DefaultWrapper Wrapper = NewWrapper(nil, nil)

// wrapper implement the Wrapper interface.
// before() will be executed before the function starts, and after()
// will be executed after the function ends.
type wrapper struct {
	before func()
	after  func()
}

func (l *wrapper) Run(f func()) {
	if l.before != nil {
		l.before()
	}
	go func() {
		if l.after != nil {
			defer l.after()
		}
		f()
	}()
}

func NewWrapper(before, after func()) Wrapper {
	return &wrapper{
		before: before,
		after:  after,
	}
}
