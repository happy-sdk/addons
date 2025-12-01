// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"sync"

	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/happy/sdk/api"
	"github.com/happy-sdk/happy/sdk/session"
)

type API struct {
	api.Provider
	mu sync.Mutex

	client *ctl.Client
}

func (api *API) Client(sess *session.Context) (*ctl.Client, error) {
	api.mu.Lock()
	defer api.mu.Unlock()

	if api.client == nil {
		client, err := ctl.Connect(sess)
		if err != nil {
			return nil, err
		}
		api.client = client
	}
	return api.client, nil
}
