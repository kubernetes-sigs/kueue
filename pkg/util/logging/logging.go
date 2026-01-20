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
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"sigs.k8s.io/kueue/pkg/util/slices"
)

// ObjectRefProvider is an interface for types that can provide a Kubernetes object reference.
type ObjectRefProvider interface {
	GetObject() client.Object
}

// GetObjectReferences converts a slice of ObjectRefProvider items to klog.ObjectRef slice.
// This is a generic utility that can work with any type that provides a Kubernetes object.
func GetObjectReferences[T ObjectRefProvider](items []T) []klog.ObjectRef {
	return slices.Map(items, func(item *T) klog.ObjectRef {
		return klog.KObj((*item).GetObject())
	})
}

// Encoder that overrides log level of expected
// expected Reconciler errors and emits them using
// with defined target level.
// Log entries are encoded using wrapped encoder,
// without any additional changes
type ErrorLogLevelOverridesEncoder struct {
	zapcore.Encoder

	TargetLevel zapcore.Level
}

func NewErrorLogLevelOverridesEncoder(encoder zapcore.Encoder) ErrorLogLevelOverridesEncoder {
	return ErrorLogLevelOverridesEncoder{
		Encoder: encoder,
		TargetLevel: zapcore.WarnLevel,
	}
}

// Build a ErrorLogLevelOverridesEncoder using provided zap options.
// Json encoder is used for if development mode option is not set (production),
// otherwise console encoder is used.
func DefaultErrorLogLevelOverridesEncoder (options* zap.Options) ErrorLogLevelOverridesEncoder {
	var encoder zapcore.Encoder

	if options.Encoder == nil {
		encoder = newZapEncoderFromOptions(options)


	} else {
		encoder = options.Encoder
	}
	return NewErrorLogLevelOverridesEncoder(encoder)
}

func newZapEncoderFromOptions(options* zap.Options) zapcore.Encoder {
		var encoderConfig zapcore.EncoderConfig
		var encoderConstructor func(zapcore.EncoderConfig) zapcore.Encoder


		if options.Development {
			encoderConstructor = zapcore.NewConsoleEncoder
			encoderConfig = zaplog.NewDevelopmentEncoderConfig()
		} else {
			encoderConstructor = zapcore.NewJSONEncoder
			encoderConfig = zaplog.NewProductionEncoderConfig()
		}
		for _, opt := range options.EncoderConfigOptions {
			opt(&encoderConfig)
		}
		return  encoderConstructor(encoderConfig)

}


func (encoder ErrorLogLevelOverridesEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
			if entry.Level == zapcore.ErrorLevel && entry.Message == "Reconciler error" {
			entry.Level = zapcore.WarnLevel
		}
		return encoder.Encoder.EncodeEntry(entry, fields)
}


func (encoder ErrorLogLevelOverridesEncoder) Clone() zapcore.Encoder {
	return ErrorLogLevelOverridesEncoder{
		Encoder: encoder.Encoder.Clone(),
		TargetLevel: encoder.TargetLevel,
	}
}
