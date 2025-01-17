// SPDX-FileCopyrightText: 2020-present Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

var root *zapLogger

const nameSep = "/"

// CustomInit replacement for the normal init
// Can be called if a configuration folder other than .onos is to be used.
// Else just call GetLogger as usual.
func CustomInit(dir string) {
	config := Config{}
	if err := customLoad(&config, dir); err != nil {
		panic(err)
	} else if err := configure(config); err != nil {
		panic(err)
	}

}

func rootInit() {
	config := Config{}
	if err := load(&config); err != nil {
		panic(err)
	} else if err := configure(config); err != nil {
		panic(err)
	}
}

// configure configures the loggers
func configure(config Config) error {
	rootLogger, err := newZapLogger(config, config.GetRootLogger())
	if err != nil {
		return err
	}
	root = rootLogger
	return nil
}

// GetLogger gets a logger by name
// If no name is provided, the caller's package name will be used if available.
// If a single name is provided, the ancestry will be determined by splitting the
// string on backslashes.
// If multiple names are provided, the set of names defines the logger ancestry.
func GetLogger(names ...string) Logger {
	if root == nil {
		rootInit()
	}
	if len(names) == 0 {
		pkg, ok := getCallerPackage()
		if !ok {
			panic("could not retrieve logger package")
		}
		names = []string{pkg}
	}
	return root.GetLogger(names...)
}

// getCallerPackage gets the package name of the calling function'ss caller
func getCallerPackage() (string, bool) {
	var pkg string
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return pkg, false
	}
	parts := strings.Split(runtime.FuncForPC(pc).Name(), ".")
	if parts[len(parts)-2][0] == '(' {
		pkg = strings.Join(parts[0:len(parts)-2], ".")
	} else {
		pkg = strings.Join(parts[0:len(parts)-1], ".")
	}
	return pkg, true
}

// SetLevel sets the root logger level
func SetLevel(level Level) {
	root.SetLevel(level)
}

// Logger represents an abstract logging interface.
type Logger interface {
	Output

	// Name returns the logger name
	Name() string

	// GetLogger gets a descendant of this Logger
	GetLogger(names ...string) Logger

	// GetLevel returns the logger's level
	GetLevel() Level

	// SetLevel sets the logger's level
	SetLevel(level Level)
}

func newZapLogger(config Config, loggerConfig LoggerConfig) (*zapLogger, error) {
	var outputs []*zapOutput
	outputConfigs := loggerConfig.GetOutputs()
	outputs = make([]*zapOutput, len(outputConfigs))
	for i, outputConfig := range outputConfigs {
		var sinkConfig SinkConfig
		if outputConfig.Sink == nil {
			return nil, fmt.Errorf("output sink not configured for output %s", outputConfig.Name)
		}
		sink, ok := config.GetSink(*outputConfig.Sink)
		if !ok {
			panic(fmt.Sprintf("unknown sink %s", *outputConfig.Sink))
		}
		sinkConfig = sink
		output, err := newZapOutput(loggerConfig, outputConfig, sinkConfig)
		if err != nil {
			return nil, err
		}
		outputs[i] = output
	}

	var level *Level
	var defaultLevel *Level
	if loggerConfig.Level != nil {
		loggerLevel := loggerConfig.GetLevel()
		level = &loggerLevel
	}

	logger := &zapLogger{
		config:       config,
		loggerConfig: loggerConfig,
		children:     make(map[string]*zapLogger),
		outputs:      outputs,
	}
	logger.level.Store(level)
	logger.defaultLevel.Store(defaultLevel)
	return logger, nil
}

// zapLogger is the default Logger implementation
type zapLogger struct {
	config       Config
	loggerConfig LoggerConfig
	children     map[string]*zapLogger
	outputs      []*zapOutput
	mu           sync.RWMutex
	level        atomic.Value
	defaultLevel atomic.Value
}

func (l *zapLogger) Name() string {
	return l.loggerConfig.Name
}

func (l *zapLogger) GetLogger(names ...string) Logger {
	if len(names) == 1 {
		names = strings.Split(names[0], nameSep)
	}

	logger := l
	for _, name := range names {
		child, err := logger.getChild(name)
		if err != nil {
			panic(err)
		}
		logger = child
	}
	return logger
}

func (l *zapLogger) getChild(name string) (*zapLogger, error) {
	l.mu.RLock()
	child, ok := l.children[name]
	l.mu.RUnlock()
	if ok {
		return child, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	child, ok = l.children[name]
	if ok {
		return child, nil
	}

	// Compute the name of the child logger
	qualifiedName := strings.Trim(fmt.Sprintf("%s%s%s", l.loggerConfig.Name, nameSep, name), nameSep)

	// Initialize the child logger's configuration if one is not set.
	loggerConfig, ok := l.config.GetLogger(qualifiedName)
	if !ok {
		loggerConfig = l.loggerConfig
		loggerConfig.Name = qualifiedName
		loggerConfig.Level = nil
	}

	// Populate the child logger configuration with outputs inherited from this logger.
	for _, output := range l.outputs {
		outputConfig, ok := loggerConfig.GetOutput(output.config.Name)
		if !ok {
			loggerConfig.Output[output.config.Name] = output.config
		} else {
			if outputConfig.Sink == nil {
				outputConfig.Sink = output.config.Sink
			}
			if outputConfig.Level == nil {
				outputConfig.Level = output.config.Level
			}
			loggerConfig.Output[outputConfig.Name] = outputConfig
		}
	}

	// Create the child logger.
	logger, err := newZapLogger(l.config, loggerConfig)
	if err != nil {
		return nil, err
	}

	// Set the default log level on the child.
	logger.setDefaultLevel(l.GetLevel())
	l.children[name] = logger
	return logger, nil
}

func (l *zapLogger) GetLevel() Level {
	level := l.level.Load().(*Level)
	if level != nil {
		return *level
	}

	defaultLevel := l.defaultLevel.Load().(*Level)
	if defaultLevel != nil {
		return *defaultLevel
	}
	return EmptyLevel
}

func (l *zapLogger) SetLevel(level Level) {
	l.level.Store(&level)
	for _, child := range l.children {
		child.setDefaultLevel(level)
	}
}

func (l *zapLogger) setDefaultLevel(level Level) {
	l.defaultLevel.Store(&level)
	if l.level.Load().(*Level) == nil {
		for _, child := range l.children {
			child.setDefaultLevel(level)
		}
	}
}

func (l *zapLogger) Debug(args ...interface{}) {
	if l.GetLevel() <= DebugLevel {
		for _, output := range l.outputs {
			output.Debug(args...)
		}
	}
}

func (l *zapLogger) Debugf(template string, args ...interface{}) {
	if l.GetLevel() <= DebugLevel {
		for _, output := range l.outputs {
			output.Debugf(template, args...)
		}
	}
}

func (l *zapLogger) Debugw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= DebugLevel {
		for _, output := range l.outputs {
			output.Debugw(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) Info(args ...interface{}) {
	if l.GetLevel() <= InfoLevel {
		for _, output := range l.outputs {
			output.Info(args...)
		}
	}
}

func (l *zapLogger) Infof(template string, args ...interface{}) {
	if l.GetLevel() <= InfoLevel {
		for _, output := range l.outputs {
			output.Infof(template, args...)
		}
	}
}

func (l *zapLogger) Infow(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= InfoLevel {
		for _, output := range l.outputs {
			output.Infow(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) Error(args ...interface{}) {
	if l.GetLevel() <= ErrorLevel {
		for _, output := range l.outputs {
			output.Error(args...)
		}
	}
}

func (l *zapLogger) Errorf(template string, args ...interface{}) {
	if l.GetLevel() <= ErrorLevel {
		for _, output := range l.outputs {
			output.Errorf(template, args...)
		}
	}
}

func (l *zapLogger) Errorw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= ErrorLevel {
		for _, output := range l.outputs {
			output.Errorw(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) Fatal(args ...interface{}) {
	if l.GetLevel() <= FatalLevel {
		for _, output := range l.outputs {
			output.Fatal(args...)
		}
	}
}

func (l *zapLogger) Fatalf(template string, args ...interface{}) {
	if l.GetLevel() <= FatalLevel {
		for _, output := range l.outputs {
			output.Fatalf(template, args...)
		}
	}
}

func (l *zapLogger) Fatalw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= FatalLevel {
		for _, output := range l.outputs {
			output.Fatalw(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) Panic(args ...interface{}) {
	if l.GetLevel() <= PanicLevel {
		for _, output := range l.outputs {
			output.Panic(args...)
		}
	}
}

func (l *zapLogger) Panicf(template string, args ...interface{}) {
	if l.GetLevel() <= PanicLevel {
		for _, output := range l.outputs {
			output.Panicf(template, args...)
		}
	}
}

func (l *zapLogger) Panicw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= PanicLevel {
		for _, output := range l.outputs {
			output.Panicw(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) DPanic(args ...interface{}) {
	if l.GetLevel() <= DPanicLevel {
		for _, output := range l.outputs {
			output.DPanic(args...)
		}
	}
}

func (l *zapLogger) DPanicf(template string, args ...interface{}) {
	if l.GetLevel() <= DPanicLevel {
		for _, output := range l.outputs {
			output.DPanicf(template, args...)
		}
	}
}

func (l *zapLogger) DPanicw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= DPanicLevel {
		for _, output := range l.outputs {
			output.DPanicw(msg, keysAndValues...)
		}
	}
}

func (l *zapLogger) Warn(args ...interface{}) {
	if l.GetLevel() <= WarnLevel {
		for _, output := range l.outputs {
			output.Warn(args...)
		}
	}
}

func (l *zapLogger) Warnf(template string, args ...interface{}) {
	if l.GetLevel() <= WarnLevel {
		for _, output := range l.outputs {
			output.Warnf(template, args...)
		}
	}
}

func (l *zapLogger) Warnw(msg string, keysAndValues ...interface{}) {
	if l.GetLevel() <= WarnLevel {
		for _, output := range l.outputs {
			output.Warnw(msg, keysAndValues...)
		}
	}
}

var _ Logger = &zapLogger{}
