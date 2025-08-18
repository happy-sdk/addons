// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"fmt"
	"time"

	"github.com/happy-sdk/addons/daemon/ipc"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

// Daemon client control service
func (api *API) controlService() *services.Service {
	svc := services.New(service.Config{
		Name:          "daemon-ctl",
		Description:   "Daemon client control service",
		RetryOnError:  false,
		MaxRetries:    0,
		RetryBackoff:  settings.Duration(5 * time.Second),
		LoaderTimeout: settings.Duration(time.Second * 5),
	})

	svc.OnStart(func(sess *session.Context) (err error) {

		// fmt.Println(daemonConfigTableString(sess))
		api.debug(sess.Log(), "starting control service")

		client, err := ipc.NewClient(sess)
		if err != nil {
			return err
		}

		if err := client.Connect(); err != nil {
			return err
		}
		api.mu.Lock()
		api.client = client
		api.mu.Unlock()

		api.info(sess.Log(), "control service connected")
		return nil
	})

	svc.OnStop(func(sess *session.Context, err error) error {
		api.mu.Lock()
		if api.client != nil {
			if err := api.client.Close(); err != nil {
				api.err(sess.Log(), err.Error())
			}
		}
		api.mu.Unlock()

		return nil
	})

	svc.Cron(func(schedule services.CronScheduler) {
		schedule.Job("heartbeat", "@every 5s", func(sess *session.Context) error {
			client, err := api.Client()
			if err != nil {
				sess.Log().Warn(fmt.Sprintf("heartbeat could not get client: %s", err.Error()))
				return nil
			}
			if !client.Connected() {
				sess.Log().Warn("heartbeat, but client not connected")
			}
			if client.ShouldHartbeat() {
				if pong, err := client.Ping(0); err != nil {
					api.err(sess.Log(), err.Error())
				} else {
					sess.Log().Debug(fmt.Sprintf("%d bytes from daemon (%s): seq=%d time=%.3f ms",
						pong.Len(),
						pong.Addr(),
						0,
						pong.Duration().Seconds()*1000,
					))
				}
			}
			return nil
		})
	})

	return svc
}
