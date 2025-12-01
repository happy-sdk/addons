// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ctl

import "github.com/happy-sdk/happy/sdk/session"

type Client struct {
}

func Connect(sess *session.Context) (*Client, error) {
	return &Client{}, nil
}
