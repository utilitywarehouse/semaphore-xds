package log

import (
	"fmt"

	elog "github.com/envoyproxy/go-control-plane/pkg/log"
	hclog "github.com/hashicorp/go-hclog"
)

// Logger - Application wide logger obj
var Logger hclog.Logger

// InitLogger - a logger for application wide use
func InitLogger(name, logLevel string) {
	Logger = hclog.New(&hclog.LoggerOptions{
		Name:  name,
		Level: hclog.LevelFromString(logLevel),
	})
}

// EnvoyLogger implements a logger to be passed to the envoy snapshot cache
var EnvoyLogger elog.Logger = &elog.LoggerFuncs{
	DebugFunc: func(s string, i ...interface{}) {
		msg := fmt.Sprintf(s, i...)
		Logger.Debug("Snapshotter", "msg", msg)
	},
	InfoFunc: func(s string, i ...interface{}) {
		msg := fmt.Sprintf(s, i...)
		Logger.Info("Snapshotter", "msg", msg)
	},
	WarnFunc: func(s string, i ...interface{}) {
		msg := fmt.Sprintf(s, i...)
		Logger.Warn("Snapshotter", "msg", msg)
	},
	ErrorFunc: func(s string, i ...interface{}) {
		msg := fmt.Sprintf(s, i...)
		Logger.Error("Snapshotter", "msg", msg)
	},
}
