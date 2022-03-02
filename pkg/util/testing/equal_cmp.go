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

package testing

import (
	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega/types"
)

// This file was adapted from https://github.com/KamikazeZirou/equal-cmp
// Original license follows:
//
// MIT License
//
// Copyright (c) 2021 KamikazeZirou
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// Equal uses go-cmp to compare actual with expected. Equal is strict about
// types when performing comparisons.
func Equal(expected interface{}, options ...cmp.Option) types.GomegaMatcher {
	return &equalCmpMatcher{
		expected: expected,
		options:  options,
	}
}

type equalCmpMatcher struct {
	expected interface{}
	options  cmp.Options
}

func (matcher *equalCmpMatcher) Match(actual interface{}) (success bool, err error) {
	return cmp.Equal(actual, matcher.expected, matcher.options), nil
}

func (matcher *equalCmpMatcher) FailureMessage(actual interface{}) (message string) {
	diff := cmp.Diff(matcher.expected, actual, matcher.options)
	return "Mismatch (-want,+got):\n" + diff
}

func (matcher *equalCmpMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	diff := cmp.Diff(matcher.expected, actual, matcher.options)
	return "Mismatch (-want,+got):\n" + diff
}
