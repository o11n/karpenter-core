/*
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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zapio"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"knative.dev/pkg/changeset"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/logging/logkey"
	"knative.dev/pkg/system"

	"github.com/aws/karpenter-core/pkg/operator/injection"
	"github.com/aws/karpenter-core/pkg/operator/options"
)

const (
	loggerCfgConfigMapName = "config-logging"
	loggerCfgConfigMapKey  = "zap-logger-config"
	loggerCfgDir           = "/etc/karpenter/logging"
	loggerCfgFilePath      = loggerCfgDir + "/zap-logger-config"
)

func DefaultZapConfig(component string) zap.Config {
	return zap.Config{
		Level:             lo.Ternary(component != "webhook", zap.NewAtomicLevelAt(zap.InfoLevel), zap.NewAtomicLevelAt(zap.ErrorLevel)),
		Development:       false,
		DisableCaller:     true,
		DisableStacktrace: true,
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},
		Encoding: "json",
		EncoderConfig: zapcore.EncoderConfig{
			MessageKey:     "message",
			LevelKey:       "level",
			TimeKey:        "time",
			NameKey:        "logger",
			CallerKey:      "caller",
			FunctionKey:    zapcore.OmitKey,
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}
}

// NewLogger returns a configured *zap.SugaredLogger
func NewLogger(ctx context.Context, component string, kubernetesInterface kubernetes.Interface) *zap.SugaredLogger {
	if logger := loggerFromFile(ctx, component); logger != nil {
		logger.Debugf("loaded log configuration from file %q", loggerCfgFilePath)
		return logger
	}
	// TODO @joinnis: Drop support for loading logging configuration from the apiserver discovered ConfigMap when
	// dropping alpha support. At that point, we will only support the environment variables and file-based config
	if logger := loggerFromConfigMap(ctx, component, kubernetesInterface); logger != nil {
		logger.Debugf("loaded log configuration from configmap %q", types.NamespacedName{Namespace: system.Namespace(), Name: loggerCfgConfigMapName})
		logger.Error("loading log configuration through the configmap is deprecated, use file or environment-variable based configuration instead")
		return logger
	}
	return defaultLogger(ctx, component)
}

func WithCommit(logger *zap.SugaredLogger) *zap.SugaredLogger {
	revision := changeset.Get()
	if revision == changeset.Unknown {
		logger.Info("Unable to read vcs.revision from binary")
		return logger
	}
	// Enrich logs with the components git revision.
	return logger.With(zap.String(logkey.Commit, revision))
}

func defaultLogger(ctx context.Context, component string) *zap.SugaredLogger {
	cfg := DefaultZapConfig(component)
	if l := options.FromContext(ctx).LogLevel; l != "" {
		// Webhook log level can only be configured directly through the zap-config
		// Webhooks are deprecated, so support for changing their log level is also deprecated
		if component != "webhook" {
			cfg.Level = lo.Must(zap.ParseAtomicLevel(l))
		}
	}
	return WithCommit(lo.Must(cfg.Build()).Sugar()).Named(component)
}

func loggerFromFile(ctx context.Context, component string) *zap.SugaredLogger {
	raw, err := os.ReadFile(loggerCfgFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Fatalf("retrieving logging configuration file from %q", loggerCfgFilePath)
	}
	cfg := DefaultZapConfig(component)
	lo.Must0(json.Unmarshal(raw, &cfg))

	raw, err = os.ReadFile(loggerCfgDir + fmt.Sprintf("/loglevel.%s", component))
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("retrieving logging controller log level file from %q", loggerCfgDir+fmt.Sprintf("/loglevel.%s", component))
	}
	if raw != nil {
		cfg.Level = lo.Must(zap.ParseAtomicLevel(string(raw)))
	}
	if l := options.FromContext(ctx).LogLevel; l != "" {
		// Webhook log level can only be configured directly through the zap-config
		// Webhooks are deprecated, so support for changing their log level is also deprecated
		if component != "webhook" {
			cfg.Level = lo.Must(zap.ParseAtomicLevel(l))
		}
	}
	return WithCommit(lo.Must(cfg.Build()).Sugar()).Named(component)
}

func loggerFromConfigMap(ctx context.Context, component string, kubernetesInterface kubernetes.Interface) *zap.SugaredLogger {
	cancelCtx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	factory := informers.NewSharedInformerFactoryWithOptions(kubernetesInterface, time.Second*30, informers.WithNamespace(system.Namespace()))
	informer := factory.Core().V1().ConfigMaps().Informer()
	factory.Start(cancelCtx.Done())

	cm, err := injection.WaitForConfigMap(cancelCtx, loggerCfgConfigMapName, informer)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		log.Fatalf("retrieving logging config map from %q", types.NamespacedName{Namespace: system.Namespace(), Name: loggerCfgConfigMapName})
	}
	cfg := DefaultZapConfig(component)
	lo.Must0(json.Unmarshal([]byte(cm.Data[loggerCfgConfigMapKey]), &cfg))

	if v := cm.Data[fmt.Sprintf("loglevel.%s", component)]; v != "" {
		cfg.Level = lo.Must(zap.ParseAtomicLevel(v))
	}
	if l := options.FromContext(ctx).LogLevel; l != "" {
		// Webhook log level can only be configured directly through the zap-config
		// Webhooks are deprecated, so support for changing their log level is also deprecated
		if component != "webhook" {
			cfg.Level = lo.Must(zap.ParseAtomicLevel(l))
		}
	}
	return WithCommit(lo.Must(cfg.Build()).Sugar()).Named(component)
}

// ConfigureGlobalLoggers sets up any package-wide loggers like "log" or "klog" that are utilized by other packages
// to use the configured *zap.SugaredLogger from the context
func ConfigureGlobalLoggers(ctx context.Context) {
	klog.SetLogger(zapr.NewLogger(logging.FromContext(ctx).Desugar()))
	w := &zapio.Writer{Log: logging.FromContext(ctx).Desugar(), Level: zap.DebugLevel}
	log.SetFlags(0)
	log.SetOutput(w)
	rest.SetDefaultWarningHandler(&logging.WarningHandler{Logger: logging.FromContext(ctx)})
}

type ignoreDebugEventsSink struct {
	name string
	sink logr.LogSink
}

func (i ignoreDebugEventsSink) Init(ri logr.RuntimeInfo) {
	i.sink.Init(ri)
}
func (i ignoreDebugEventsSink) Enabled(level int) bool { return i.sink.Enabled(level) }
func (i ignoreDebugEventsSink) Info(level int, msg string, keysAndValues ...interface{}) {
	// ignore debug "events" logs
	if level == 1 && i.name == "events" {
		return
	}
	i.sink.Info(level, msg, keysAndValues...)
}
func (i ignoreDebugEventsSink) Error(err error, msg string, keysAndValues ...interface{}) {
	i.sink.Error(err, msg, keysAndValues...)
}
func (i ignoreDebugEventsSink) WithValues(keysAndValues ...interface{}) logr.LogSink {
	return i.sink.WithValues(keysAndValues...)
}
func (i ignoreDebugEventsSink) WithName(name string) logr.LogSink {
	return &ignoreDebugEventsSink{name: name, sink: i.sink.WithName(name)}
}

// IgnoreDebugEvents wraps the logger with one that ignores any debug logs coming from a logger named "events".  This
// prevents every event we write from creating a debug log which spams the log file during scale-ups due to recording
// pod scheduling decisions as events for visibility.
func IgnoreDebugEvents(logger logr.Logger) logr.Logger {
	return logr.New(&ignoreDebugEventsSink{sink: logger.GetSink()})
}
